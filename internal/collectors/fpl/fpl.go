package fpl

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fengyuanluo/proxycollector/internal/collector"
	"github.com/fengyuanluo/proxycollector/internal/config"
	"github.com/fengyuanluo/proxycollector/internal/fetch"
	"github.com/fengyuanluo/proxycollector/internal/model"
)

const collectorName = "fpl"

type sourceCache struct {
	etag         string
	lastModified string
	proxies      []model.Proxy
	report       collector.Report
}

type Collector struct {
	cfg    config.FPLConfig
	client *fetch.Client

	mu    sync.Mutex
	cache map[string]sourceCache
}

func New(cfg config.FPLConfig, client *fetch.Client) *Collector {
	return &Collector{cfg: cfg, client: client, cache: map[string]sourceCache{}}
}

func (c *Collector) Name() string { return collectorName }

func (c *Collector) RefreshInterval() time.Duration { return c.cfg.RefreshInterval.Duration }

func (c *Collector) Sources() []string {
	out := make([]string, len(c.cfg.Sources))
	for i, source := range c.cfg.Sources {
		out[i] = source.Name
	}
	return out
}

func (c *Collector) Close() {}

func (c *Collector) Collect(ctx context.Context) []collector.Result {
	results := make([]collector.Result, len(c.cfg.Sources))
	if len(results) == 0 || ctx.Err() != nil {
		return results
	}
	workers := min(c.cfg.FetchConcurrency, len(results))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				results[index] = c.collectSource(ctx, c.cfg.Sources[index])
			}
		}()
	}
	for index := range c.cfg.Sources {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return nil
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}

	remaining := c.cfg.TotalMaxCandidates
	for i := range results {
		if len(results[i].Proxies) > remaining {
			results[i].Proxies = results[i].Proxies[:max(remaining, 0)]
			results[i].Report.Partial = true
			results[i].Report.AddSkip("total_candidate_limit_reached")
		}
		remaining -= len(results[i].Proxies)
		results[i].Report.Imported = len(results[i].Proxies)
	}
	return results
}

func (c *Collector) collectSource(ctx context.Context, source config.FPLSourceConfig) (result collector.Result) {
	started := time.Now().UTC()
	report := collector.Report{
		Collector:   collectorName,
		Source:      source.Name,
		StartedAt:   started,
		SkipReasons: map[string]int{},
	}
	result = collector.Result{Source: source.Name, Report: report}
	defer func() {
		result.Report.FinishedAt = time.Now().UTC()
		result.Report.Imported = len(result.Proxies)
	}()

	cached, hasCache := c.loadCache(source.Name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		result.Report.Error = "build source request"
		return result
	}
	if hasCache {
		if cached.etag != "" {
			req.Header.Set("If-None-Match", cached.etag)
		}
		if cached.lastModified != "" {
			req.Header.Set("If-Modified-Since", cached.lastModified)
		}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		result.Report.Error = "fetch source: " + fetch.Error(err)
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		if !hasCache {
			result.Report.Error = "source returned 304 without an in-memory cache"
			return result
		}
		result.Proxies = append([]model.Proxy(nil), cached.proxies...)
		result.Report.Total = cached.report.Total
		result.Report.Skipped = cached.report.Skipped
		result.Report.SkipReasons = cloneCounts(cached.report.SkipReasons)
		result.Report.Partial = cached.report.Partial
		return result
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Report.Error = "source status " + resp.Status
		return result
	}
	body, err := readLimited(resp.Body, source.MaxResponseBytes.Bytes)
	if err != nil {
		result.Report.Error = err.Error()
		return result
	}
	result.Proxies = parseSource(ctx, bytes.NewReader(body), source, &result.Report)
	if ctx.Err() != nil {
		return collector.Result{}
	}
	if result.Report.Error == "" {
		c.storeCache(source.Name, sourceCache{
			etag:         boundedHeader(resp.Header.Get("ETag")),
			lastModified: boundedHeader(resp.Header.Get("Last-Modified")),
			proxies:      append([]model.Proxy(nil), result.Proxies...),
			report:       result.Report,
		})
	}
	return result
}

func parseSource(ctx context.Context, input io.Reader, source config.FPLSourceConfig, report *collector.Report) []model.Proxy {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), config.MaxCandidateTextBytes)
	proxies := make([]model.Proxy, 0, min(source.MaxCandidates, 1024))
	seen := make(map[string]struct{}, min(source.MaxCandidates, 1024))
	for scanner.Scan() {
		if ctx.Err() != nil {
			report.Error = ctx.Err().Error()
			return proxies
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		report.Total++
		proxy, reason, ok := parseLine(line, source)
		if !ok {
			report.AddSkip(reason)
			continue
		}
		key := proxy.URL()
		if _, duplicate := seen[key]; duplicate {
			report.AddSkip("duplicate_proxy")
			continue
		}
		if len(proxies) >= source.MaxCandidates {
			report.Partial = true
			report.AddSkip("candidate_limit_reached")
			break
		}
		seen[key] = struct{}{}
		proxies = append(proxies, proxy)
	}
	if err := scanner.Err(); err != nil {
		report.Error = "scan proxy list: " + err.Error()
	}
	return proxies
}

func parseLine(line string, source config.FPLSourceConfig) (model.Proxy, string, bool) {
	if source.Format == config.FPLFormatURLList {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "socks4://") {
			return model.Proxy{}, "unsupported_socks4", false
		}
		proxy, err := model.Parse(line)
		if err != nil {
			return model.Proxy{}, "invalid_proxy_url", false
		}
		return proxy, "", true
	}
	if strings.Contains(line, "://") {
		return model.Proxy{}, "invalid_host_port", false
	}
	host, portText, err := net.SplitHostPort(line)
	if err != nil {
		return model.Proxy{}, "invalid_host_port", false
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return model.Proxy{}, "invalid_host_port", false
	}
	proxy, err := model.New(source.Protocol, host, port, "", "")
	if err != nil {
		return model.Proxy{}, "invalid_host_port", false
	}
	return proxy, "", true
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("source response exceeds %d bytes", limit)
	}
	return data, nil
}

func boundedHeader(value string) string {
	if len(value) > 4096 {
		return ""
	}
	return value
}

func (c *Collector) loadCache(source string) (sourceCache, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cached, ok := c.cache[source]
	cached.proxies = append([]model.Proxy(nil), cached.proxies...)
	cached.report.SkipReasons = cloneCounts(cached.report.SkipReasons)
	return cached, ok
}

func (c *Collector) storeCache(source string, cached sourceCache) {
	cached.proxies = append([]model.Proxy(nil), cached.proxies...)
	cached.report.SkipReasons = cloneCounts(cached.report.SkipReasons)
	c.mu.Lock()
	c.cache[source] = cached
	c.mu.Unlock()
}

func cloneCounts(input map[string]int) map[string]int {
	output := make(map[string]int, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
