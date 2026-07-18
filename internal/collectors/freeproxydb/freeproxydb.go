package freeproxydb

import (
	"context"
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
	collectorName       = "freeproxydb"
	defaultSource       = "default"
	maxResponseBytes    = 4 << 20
	maxRateLimitRetries = 3
	maxRetryAfter       = 5 * time.Minute
	maxInputFactor      = 4
)

var retryDelays = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}

type Collector struct {
	cfg    config.FreeProxyDBConfig
	client *fetch.Client
	sleep  func(context.Context, time.Duration) error
}

func New(cfg config.FreeProxyDBConfig, client *fetch.Client) *Collector {
	return &Collector{cfg: cfg, client: client, sleep: sleepContext}
}

func (c *Collector) Name() string                   { return collectorName }
func (c *Collector) RefreshInterval() time.Duration { return c.cfg.RefreshInterval.Duration }
func (c *Collector) Sources() []string              { return []string{defaultSource} }
func (c *Collector) Close()                         {}

type searchResponse struct {
	Data struct {
		TotalCount int           `json:"total_count"`
		Data       []proxyRecord `json:"data"`
	} `json:"data"`
}

type proxyRecord struct {
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c *Collector) Collect(ctx context.Context) []collector.Result {
	if ctx.Err() != nil {
		return nil
	}
	result := c.collect(ctx)
	if ctx.Err() != nil {
		return nil
	}
	return []collector.Result{result}
}

func (c *Collector) collect(ctx context.Context) (result collector.Result) {
	started := time.Now().UTC()
	result = collector.Result{
		Source: defaultSource,
		Report: collector.Report{
			Collector:   collectorName,
			Source:      defaultSource,
			StartedAt:   started,
			SkipReasons: map[string]int{},
		},
	}
	defer func() {
		result.Report.FinishedAt = time.Now().UTC()
		result.Report.Imported = len(result.Proxies)
	}()

	first, err := c.fetchPage(ctx, 1)
	if err != nil {
		result.Report.Error = err.Error()
		return result
	}
	if first.Data.TotalCount < 0 {
		result.Report.Error = fmt.Sprintf("freeproxydb total_count must not be negative: %d", first.Data.TotalCount)
		return result
	}
	seen := map[string]struct{}{}
	recordLimit := c.cfg.MaxCandidates * maxInputFactor
	if recordLimit < c.cfg.PageSize {
		recordLimit = c.cfg.PageSize
	}
	if c.appendRecords(first.Data.Data, &result, seen, recordLimit) {
		return result
	}
	totalPages := pages(first.Data.TotalCount, c.cfg.PageSize)
	for page := 2; page <= totalPages; page++ {
		if err := c.sleep(ctx, c.cfg.RequestInterval.Duration); err != nil {
			result.Report.Error = err.Error()
			return result
		}
		response, err := c.fetchPage(ctx, page)
		if err != nil {
			result.Report.Error = err.Error()
			return result
		}
		if len(response.Data.Data) == 0 {
			result.Report.Error = fmt.Sprintf("freeproxydb page %d is empty before total_count=%d is exhausted", page, first.Data.TotalCount)
			return result
		}
		if c.appendRecords(response.Data.Data, &result, seen, recordLimit) {
			return result
		}
	}
	return result
}

func (c *Collector) appendRecords(records []proxyRecord, result *collector.Result, seen map[string]struct{}, recordLimit int) bool {
	for _, record := range records {
		if result.Report.Total >= recordLimit {
			result.Report.Partial = true
			result.Report.AddSkip("input_record_limit_reached")
			return true
		}
		result.Report.Total++
		protocol := strings.ToLower(strings.TrimSpace(record.Protocol))
		if protocol != model.ProtocolHTTP && protocol != model.ProtocolSOCKS5 {
			if protocol == "" {
				protocol = "unknown"
			}
			result.Report.AddSkip("unsupported_" + safeReason(protocol))
			continue
		}
		proxy, err := model.New(protocol, record.IP, record.Port, record.Username, record.Password)
		if err != nil {
			result.Report.AddSkip("invalid_proxy")
			continue
		}
		key := proxy.URL()
		if _, duplicate := seen[key]; duplicate {
			result.Report.AddSkip("duplicate_proxy")
			continue
		}
		if len(result.Proxies) >= c.cfg.MaxCandidates {
			result.Report.Partial = true
			result.Report.AddSkip("candidate_limit_reached")
			return true
		}
		seen[key] = struct{}{}
		result.Proxies = append(result.Proxies, proxy)
	}
	return false
}

func (c *Collector) fetchPage(ctx context.Context, page int) (searchResponse, error) {
	var result searchResponse
	for attempt := 0; ; attempt++ {
		rawURL, err := c.searchURL(page)
		if err != nil {
			return result, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return result, err
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return result, fmt.Errorf("fetch freeproxydb page %d: %s", page, fetch.Error(err))
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			delay := retryDelay(resp, attempt)
			_ = resp.Body.Close()
			if attempt >= maxRateLimitRetries {
				return result, fmt.Errorf("freeproxydb page %d rate limited after %d retries", page, maxRateLimitRetries)
			}
			if err := c.sleep(ctx, delay); err != nil {
				return result, err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			return result, fmt.Errorf("freeproxydb page %d status %s", page, resp.Status)
		}
		body, readErr := readLimited(resp.Body, maxResponseBytes)
		closeErr := resp.Body.Close()
		if readErr != nil {
			return result, readErr
		}
		if closeErr != nil {
			return result, closeErr
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return result, fmt.Errorf("decode freeproxydb page %d: %w", page, err)
		}
		return result, nil
	}
}

func (c *Collector) searchURL(page int) (string, error) {
	u, err := url.Parse(strings.TrimRight(c.cfg.BaseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid freeproxydb base_url")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/proxy/search"
	query := u.Query()
	query.Set("country", "")
	query.Set("protocol", "")
	query.Set("anonymity", "")
	query.Set("speed", "0,60")
	query.Set("https", "0")
	query.Set("page_index", strconv.Itoa(page))
	query.Set("page_size", strconv.Itoa(c.cfg.PageSize))
	query.Set("order_by", "id")
	query.Set("order_dir", "desc")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func retryDelay(response *http.Response, attempt int) time.Duration {
	raw := strings.TrimSpace(response.Header.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		delay := time.Duration(seconds) * time.Second
		if delay > maxRetryAfter {
			return maxRetryAfter
		}
		return delay
	}
	if retryAt, err := http.ParseTime(raw); err == nil {
		delay := time.Until(retryAt)
		if delay > maxRetryAfter {
			return maxRetryAfter
		}
		if delay > 0 {
			return delay
		}
	}
	if attempt >= len(retryDelays) {
		return retryDelays[len(retryDelays)-1]
	}
	return retryDelays[attempt]
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("freeproxydb response exceeds %d bytes", limit)
	}
	return data, nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func pages(total, pageSize int) int {
	if total <= 0 || pageSize <= 0 {
		return 0
	}
	return 1 + (total-1)/pageSize
}

func safeReason(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return "invalid"
		}
	}
	if value == "" {
		return "unknown"
	}
	return value
}
