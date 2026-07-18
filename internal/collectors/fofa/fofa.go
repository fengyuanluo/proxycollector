package fofa

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fengyuanluo/proxycollector/internal/collector"
	"github.com/fengyuanluo/proxycollector/internal/config"
	"github.com/fengyuanluo/proxycollector/internal/fetch"
	"github.com/fengyuanluo/proxycollector/internal/model"
)

const (
	collectorName   = "fofa"
	maxResponseSize = 16 << 20
)

type Collector struct {
	cfg    config.FOFAConfig
	client *fetch.Client
}

func New(cfg config.FOFAConfig, client *fetch.Client) *Collector {
	return &Collector{cfg: cfg, client: client}
}

func (c *Collector) Name() string                   { return collectorName }
func (c *Collector) RefreshInterval() time.Duration { return c.cfg.RefreshInterval.Duration }
func (c *Collector) Close()                         {}

func (c *Collector) Sources() []string {
	out := make([]string, len(c.cfg.Queries))
	for i, query := range c.cfg.Queries {
		out[i] = query.Name
	}
	return out
}

func (c *Collector) Collect(ctx context.Context) []collector.Result {
	results := make([]collector.Result, 0, len(c.cfg.Queries))
	remaining := c.cfg.TotalMaxCandidates
	for _, query := range c.cfg.Queries {
		if ctx.Err() != nil {
			return nil
		}
		limit := min(c.cfg.Size, remaining)
		result := c.collectQuery(ctx, query, limit)
		results = append(results, result)
		remaining -= len(result.Proxies)
	}
	return results
}

func (c *Collector) collectQuery(ctx context.Context, query config.FOFAQueryConfig, limit int) (result collector.Result) {
	started := time.Now().UTC()
	report := collector.Report{Collector: collectorName, Source: query.Name, StartedAt: started, SkipReasons: map[string]int{}}
	result = collector.Result{Source: query.Name, Report: report}
	defer func() {
		result.Report.FinishedAt = time.Now().UTC()
		result.Report.Imported = len(result.Proxies)
	}()
	if limit <= 0 {
		result.Report.Partial = true
		result.Report.AddSkip("total_candidate_limit_reached")
		return result
	}

	u, err := url.Parse(strings.TrimRight(c.cfg.BaseURL, "/") + "/api/v1/search/all")
	if err != nil {
		result.Report.Error = "parse FOFA base URL"
		return result
	}
	params := u.Query()
	params.Set("key", c.cfg.Key)
	params.Set("qbase64", base64.StdEncoding.EncodeToString([]byte(query.Query)))
	params.Set("size", strconv.Itoa(min(c.cfg.Size, limit)))
	params.Set("fields", query.Fields)
	u.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		result.Report.Error = "build FOFA request"
		return result
	}
	resp, err := c.client.Do(req)
	if err != nil {
		result.Report.Error = "fetch FOFA query: " + fetch.Error(err)
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Report.Error = "FOFA status " + resp.Status
		return result
	}
	body, err := readLimited(resp.Body, maxResponseSize)
	if err != nil {
		result.Report.Error = err.Error()
		return result
	}
	var response struct {
		Error   bool    `json:"error"`
		Errmsg  string  `json:"errmsg"`
		Results [][]any `json:"results"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		result.Report.Error = "decode FOFA response: " + err.Error()
		return result
	}
	if response.Error {
		result.Report.Error = "FOFA error: " + truncate(response.Errmsg, 4096)
		return result
	}
	fields := splitFields(query.Fields)
	seen := map[string]struct{}{}
	for _, row := range response.Results {
		if ctx.Err() != nil {
			return collector.Result{}
		}
		result.Report.Total++
		values := map[string]string{}
		for i, field := range fields {
			if i < len(row) {
				values[field] = fmt.Sprint(row[i])
			}
		}
		host := firstNonEmpty(values["ip"], hostFromURL(values["host"]), values["host"])
		port, _ := strconv.Atoi(values["port"])
		proxy, err := model.New(query.Protocol, host, port, "", "")
		if err != nil {
			result.Report.AddSkip("invalid_host_or_port")
			continue
		}
		key := proxy.URL()
		if _, duplicate := seen[key]; duplicate {
			result.Report.AddSkip("duplicate_proxy")
			continue
		}
		if len(result.Proxies) >= limit {
			result.Report.Partial = true
			result.Report.AddSkip("total_candidate_limit_reached")
			break
		}
		seen[key] = struct{}{}
		result.Proxies = append(result.Proxies, proxy)
	}
	return result
}

func splitFields(value string) []string {
	parts := strings.Split(value, ",")
	out := parts[:0]
	for _, part := range parts {
		if field := strings.TrimSpace(part); field != "" {
			out = append(out, field)
		}
	}
	return out
}

func hostFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err == nil {
		return u.Hostname()
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("FOFA response exceeds %d bytes", limit)
	}
	return data, nil
}
