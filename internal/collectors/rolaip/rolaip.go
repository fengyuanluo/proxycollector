package rolaip

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
	collectorName       = "rolaip"
	defaultSource       = "default"
	maxResponseBytes    = 4 << 20
	maxRateLimitRetries = 3
	maxRetryAfter       = 5 * time.Minute
	maxInputFactor      = 2
	maxPagesPerRefresh  = 50
)

var retryDelays = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}

type Collector struct {
	cfg    config.RolaIPConfig
	client *fetch.Client
	sleep  func(context.Context, time.Duration) error
}

func New(cfg config.RolaIPConfig, client *fetch.Client) *Collector {
	return &Collector{cfg: cfg, client: client, sleep: sleepContext}
}

func (c *Collector) Name() string                   { return collectorName }
func (c *Collector) RefreshInterval() time.Duration { return c.cfg.RefreshInterval.Duration }
func (c *Collector) Sources() []string              { return []string{defaultSource} }
func (c *Collector) Close()                         {}

type listResponse struct {
	Data       []proxyRecord `json:"data"`
	Pagination pagination    `json:"pagination"`
}

type pagination struct {
	Page       int `json:"page"`
	PageSize   int `json:"pageSize"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}

type proxyRecord struct {
	IP        string   `json:"ip"`
	Port      int      `json:"port"`
	Protocols []string `json:"protocols"`
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
	result = collector.Result{
		Source: defaultSource,
		Report: collector.Report{
			Collector:   collectorName,
			Source:      defaultSource,
			StartedAt:   time.Now().UTC(),
			SkipReasons: map[string]int{},
		},
	}
	defer func() {
		result.Report.FinishedAt = time.Now().UTC()
		result.Report.Imported = len(result.Proxies)
		if result.Report.Error != "" && len(result.Proxies) > 0 {
			result.Report.Partial = true
		}
	}()

	seen := map[string]struct{}{}
	recordLimit := c.cfg.MaxCandidates * maxInputFactor
	first, err := c.fetchPage(ctx, 1)
	if err != nil {
		result.Report.Error = err.Error()
		return result
	}
	if err := validatePagination(first.Pagination, 1); err != nil {
		result.Report.Error = err.Error()
		return result
	}
	if len(first.Data) == 0 && first.Pagination.Total > 0 {
		result.Report.Error = "rolaip page 1 is empty before pagination is exhausted"
		return result
	}
	if c.appendRecords(first.Data, &result, seen, recordLimit) {
		return result
	}

	pageLimit := min(pages(recordLimit, c.cfg.PageSize), maxPagesPerRefresh)
	totalPages := first.Pagination.TotalPages
	if totalPages > pageLimit {
		totalPages = pageLimit
	}
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
		if err := validatePagination(response.Pagination, page); err != nil {
			result.Report.Error = err.Error()
			return result
		}
		if len(response.Data) == 0 {
			result.Report.Error = fmt.Sprintf("rolaip page %d is empty before pagination is exhausted", page)
			return result
		}
		if c.appendRecords(response.Data, &result, seen, recordLimit) {
			return result
		}
	}
	if first.Pagination.TotalPages > pageLimit {
		result.Report.Partial = true
		result.Report.AddSkip("input_record_limit_reached")
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
		protocols := supportedProtocols(record.Protocols)
		if len(protocols) == 0 {
			result.Report.Total++
			if containsProtocol(record.Protocols, "socks4") {
				result.Report.AddSkip("unsupported_socks4")
			} else {
				result.Report.AddSkip("unsupported_protocol")
			}
			continue
		}
		for _, protocol := range protocols {
			result.Report.Total++
			proxy, err := model.New(protocol, record.IP, record.Port, "", "")
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
	}
	return false
}

func supportedProtocols(protocols []string) []string {
	hasHTTP := containsProtocol(protocols, "http") || containsProtocol(protocols, "https")
	hasSOCKS5 := containsProtocol(protocols, "socks5")
	result := make([]string, 0, 2)
	if hasHTTP {
		result = append(result, model.ProtocolHTTP)
	}
	if hasSOCKS5 {
		result = append(result, model.ProtocolSOCKS5)
	}
	return result
}

func containsProtocol(protocols []string, target string) bool {
	for _, protocol := range protocols {
		if strings.EqualFold(strings.TrimSpace(protocol), target) {
			return true
		}
	}
	return false
}

func (c *Collector) fetchPage(ctx context.Context, page int) (listResponse, error) {
	var result listResponse
	for attempt := 0; ; attempt++ {
		rawURL, err := c.pageURL(page)
		if err != nil {
			return result, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return result, err
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return result, fmt.Errorf("fetch rolaip page %d: %s", page, fetch.Error(err))
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			delay := retryDelay(resp, attempt)
			_ = resp.Body.Close()
			if attempt >= maxRateLimitRetries {
				return result, fmt.Errorf("rolaip page %d rate limited after %d retries", page, maxRateLimitRetries)
			}
			if err := c.sleep(ctx, delay); err != nil {
				return result, err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			return result, fmt.Errorf("rolaip page %d status %s", page, resp.Status)
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
			return result, fmt.Errorf("decode rolaip page %d: %w", page, err)
		}
		return result, nil
	}
}

func (c *Collector) pageURL(page int) (string, error) {
	u, err := url.Parse(strings.TrimRight(c.cfg.BaseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid rolaip base_url")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/proxies"
	query := u.Query()
	query.Set("page", strconv.Itoa(page))
	query.Set("pageSize", strconv.Itoa(c.cfg.PageSize))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func validatePagination(value pagination, expectedPage int) error {
	if value.Page != expectedPage || value.PageSize < 1 || value.Total < 0 || value.TotalPages < 0 {
		return fmt.Errorf("rolaip page %d returned invalid pagination", expectedPage)
	}
	if value.Total > 0 && value.TotalPages < 1 {
		return fmt.Errorf("rolaip page %d returned invalid pagination", expectedPage)
	}
	return nil
}

func retryDelay(response *http.Response, attempt int) time.Duration {
	raw := strings.TrimSpace(response.Header.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		return min(time.Duration(seconds)*time.Second, maxRetryAfter)
	}
	if retryAt, err := http.ParseTime(raw); err == nil {
		delay := time.Until(retryAt)
		if delay > 0 {
			return min(delay, maxRetryAfter)
		}
	}
	return retryDelays[min(attempt, len(retryDelays)-1)]
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("rolaip response exceeds %d bytes", limit)
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
	return (total + pageSize - 1) / pageSize
}
