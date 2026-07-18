package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultWebListen                   = "0.0.0.0:27298"
	DefaultOutputDirectory             = "./data"
	DefaultOutputFilename              = "proxies.txt"
	StateFilename                      = ".proxycollector-state.json"
	DefaultFetchTimeout                = 30 * time.Second
	DefaultRefreshInterval             = 6 * time.Hour
	MinRefreshInterval                 = time.Minute
	DefaultFPLMaxResponseBytes         = 16 << 20
	MaxFPLMaxResponseBytes             = 64 << 20
	DefaultFPLSourceCandidates         = 1000
	DefaultFPLTotalCandidates          = 10000
	MaxFPLTotalCandidates              = 50000
	DefaultFPLFetchConcurrency         = 4
	MaxFPLFetchConcurrency             = 16
	MaxFPLSources                      = 64
	DefaultFOFABaseURL                 = "http://fofa.icu"
	DefaultFOFASize                    = 100
	MaxFOFASize                        = 10000
	DefaultFOFATotalCandidates         = 10000
	MaxFOFAQueries                     = 32
	DefaultFreeProxyDBBaseURL          = "https://freeproxydb.com/api"
	DefaultFreeProxyDBPageSize         = 100
	MaxFreeProxyDBPageSize             = 100
	DefaultFreeProxyDBDelay            = time.Second
	DefaultFreeProxyDBCandidates       = 10000
	MaxFreeProxyDBCandidates           = 10000
	MaxConfigBytes               int64 = 4 << 20
	MaxCandidateTextBytes              = 1 << 20
	MaxNameBytes                       = 64
	MaxQueryBytes                      = 4 << 10
	MaxFieldsBytes                     = 1 << 10
	MaxURLBytes                        = 16 << 10
)

const (
	FPLFormatURLList  = "url_list"
	FPLFormatHostPort = "host_port"
)

type Duration struct{ time.Duration }

func (d Duration) MarshalYAML() (any, error) { return d.Duration.String(), nil }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var value string
	if err := node.Decode(&value); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

type ByteSize struct{ Bytes int64 }

func (b *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	var text string
	if err := node.Decode(&text); err == nil {
		parsed, err := ParseByteSize(text)
		if err != nil {
			return err
		}
		b.Bytes = parsed
		return nil
	}
	var number int64
	if err := node.Decode(&number); err != nil {
		return fmt.Errorf("byte size must be a number or size string")
	}
	b.Bytes = number
	return nil
}

func ParseByteSize(value string) (int64, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return 0, nil
	}
	multiplier := int64(1)
	for _, suffix := range []struct {
		name string
		mult int64
	}{
		{"GB", 1000 * 1000 * 1000}, {"MB", 1000 * 1000}, {"KB", 1000},
		{"G", 1000 * 1000 * 1000}, {"M", 1000 * 1000}, {"K", 1000}, {"B", 1},
	} {
		if strings.HasSuffix(value, suffix.name) {
			multiplier = suffix.mult
			value = strings.TrimSpace(strings.TrimSuffix(value, suffix.name))
			break
		}
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number < 0 {
		return 0, fmt.Errorf("invalid byte size %q", value)
	}
	return int64(number * float64(multiplier)), nil
}

type Config struct {
	Web        WebConfig        `yaml:"web"`
	Output     OutputConfig     `yaml:"output"`
	Fetch      FetchConfig      `yaml:"fetch"`
	Refresh    RefreshConfig    `yaml:"refresh"`
	Logging    LoggingConfig    `yaml:"logging"`
	Collectors CollectorsConfig `yaml:"collectors"`
}

type WebConfig struct {
	Listen     string `yaml:"listen"`
	PathPrefix string `yaml:"path_prefix"`
}

type OutputConfig struct {
	Directory string `yaml:"directory"`
	Filename  string `yaml:"filename"`
}

type FetchConfig struct {
	ProxyURL string   `yaml:"proxy_url"`
	Timeout  Duration `yaml:"timeout"`
}

type RefreshConfig struct {
	JitterRatio float64 `yaml:"jitter_ratio"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type CollectorsConfig struct {
	FPL         *FPLConfig         `yaml:"fpl"`
	FOFA        *FOFAConfig        `yaml:"fofa"`
	FreeProxyDB *FreeProxyDBConfig `yaml:"freeproxydb"`
}

type FPLConfig struct {
	RefreshInterval    Duration          `yaml:"refresh_interval"`
	MaxResponseBytes   ByteSize          `yaml:"max_response_bytes"`
	TotalMaxCandidates int               `yaml:"total_max_candidates"`
	FetchConcurrency   int               `yaml:"fetch_concurrency"`
	Sources            []FPLSourceConfig `yaml:"sources"`
}

type FPLSourceConfig struct {
	Name             string   `yaml:"name"`
	URL              string   `yaml:"url"`
	Format           string   `yaml:"format"`
	Protocol         string   `yaml:"protocol"`
	MaxResponseBytes ByteSize `yaml:"max_response_bytes"`
	MaxCandidates    int      `yaml:"max_candidates"`
}

type FOFAConfig struct {
	BaseURL            string            `yaml:"base_url"`
	Key                string            `yaml:"key"`
	Size               int               `yaml:"size"`
	TotalMaxCandidates int               `yaml:"total_max_candidates"`
	RefreshInterval    Duration          `yaml:"refresh_interval"`
	Queries            []FOFAQueryConfig `yaml:"queries"`
}

type FOFAQueryConfig struct {
	Name     string `yaml:"name"`
	Protocol string `yaml:"protocol"`
	Query    string `yaml:"query"`
	Fields   string `yaml:"fields"`
}

type FreeProxyDBConfig struct {
	BaseURL         string   `yaml:"base_url"`
	RefreshInterval Duration `yaml:"refresh_interval"`
	PageSize        int      `yaml:"page_size"`
	RequestInterval Duration `yaml:"request_interval"`
	MaxCandidates   int      `yaml:"max_candidates"`
}

func Load(filename string) (*Config, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	limited := io.LimitReader(file, MaxConfigBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxConfigBytes {
		return nil, fmt.Errorf("config file exceeds %d bytes", MaxConfigBytes)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("config must contain exactly one YAML document")
		}
		return nil, err
	}
	cfg.ApplyDefaults()
	return &cfg, nil
}

func (c *Config) ApplyDefaults() {
	if c.Web.Listen == "" {
		c.Web.Listen = DefaultWebListen
	}
	if c.Web.PathPrefix == "" {
		c.Web.PathPrefix = "/"
	}
	if c.Output.Directory == "" {
		c.Output.Directory = DefaultOutputDirectory
	}
	if c.Output.Filename == "" {
		c.Output.Filename = DefaultOutputFilename
	}
	if c.Fetch.Timeout.Duration == 0 {
		c.Fetch.Timeout.Duration = DefaultFetchTimeout
	}
	if c.Refresh.JitterRatio == 0 {
		c.Refresh.JitterRatio = 0.1
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "text"
	}
	if c.Collectors.FPL != nil {
		applyFPLDefaults(c.Collectors.FPL)
	}
	if c.Collectors.FOFA != nil {
		applyFOFADefaults(c.Collectors.FOFA)
	}
	if c.Collectors.FreeProxyDB != nil {
		applyFreeProxyDBDefaults(c.Collectors.FreeProxyDB)
	}
}

func applyFPLDefaults(c *FPLConfig) {
	if c.RefreshInterval.Duration == 0 {
		c.RefreshInterval.Duration = DefaultRefreshInterval
	}
	if c.MaxResponseBytes.Bytes == 0 {
		c.MaxResponseBytes.Bytes = DefaultFPLMaxResponseBytes
	}
	if c.TotalMaxCandidates == 0 {
		c.TotalMaxCandidates = DefaultFPLTotalCandidates
	}
	if c.FetchConcurrency == 0 {
		c.FetchConcurrency = DefaultFPLFetchConcurrency
	}
	if len(c.Sources) == 0 {
		c.Sources = DefaultFPLSources()
	}
	for i := range c.Sources {
		source := &c.Sources[i]
		if source.Format == "" {
			source.Format = FPLFormatURLList
		}
		if source.MaxResponseBytes.Bytes == 0 {
			source.MaxResponseBytes = c.MaxResponseBytes
		}
		if source.MaxCandidates == 0 {
			source.MaxCandidates = DefaultFPLSourceCandidates
		}
	}
}

func applyFOFADefaults(c *FOFAConfig) {
	if c.BaseURL == "" {
		c.BaseURL = DefaultFOFABaseURL
	}
	if c.Size == 0 {
		c.Size = DefaultFOFASize
	}
	if c.TotalMaxCandidates == 0 {
		c.TotalMaxCandidates = DefaultFOFATotalCandidates
	}
	if c.RefreshInterval.Duration == 0 {
		c.RefreshInterval.Duration = DefaultRefreshInterval
	}
	if len(c.Queries) == 0 {
		c.Queries = DefaultFOFAQueries()
	}
}

func applyFreeProxyDBDefaults(c *FreeProxyDBConfig) {
	if c.BaseURL == "" {
		c.BaseURL = DefaultFreeProxyDBBaseURL
	}
	if c.RefreshInterval.Duration == 0 {
		c.RefreshInterval.Duration = DefaultRefreshInterval
	}
	if c.PageSize == 0 {
		c.PageSize = DefaultFreeProxyDBPageSize
	}
	if c.RequestInterval.Duration == 0 {
		c.RequestInterval.Duration = DefaultFreeProxyDBDelay
	}
	if c.MaxCandidates == 0 {
		c.MaxCandidates = DefaultFreeProxyDBCandidates
	}
}

type CheckResult struct {
	Errors           []string
	Warnings         []string
	ActiveCollectors []string
}

func (r CheckResult) OK() bool { return len(r.Errors) == 0 }

func (c *Config) Check() CheckResult {
	c.ApplyDefaults()
	result := CheckResult{}
	if err := validateListen(c.Web.Listen); err != nil {
		result.Errors = append(result.Errors, "web.listen: "+err.Error())
	}
	if err := validatePathPrefix(c.Web.PathPrefix); err != nil {
		result.Errors = append(result.Errors, "web.path_prefix: "+err.Error())
	}
	if strings.TrimSpace(c.Output.Directory) == "" || strings.ContainsRune(c.Output.Directory, '\x00') {
		result.Errors = append(result.Errors, "output.directory must be a valid non-empty path")
	}
	if err := validateFilename(c.Output.Filename); err != nil {
		result.Errors = append(result.Errors, "output.filename: "+err.Error())
	}
	if c.Fetch.Timeout.Duration <= 0 {
		result.Errors = append(result.Errors, "fetch.timeout must be positive")
	}
	if c.Fetch.ProxyURL != "" {
		if err := validateProxyURL(c.Fetch.ProxyURL); err != nil {
			result.Errors = append(result.Errors, "fetch.proxy_url: "+err.Error())
		}
	}
	if c.Refresh.JitterRatio < 0 || c.Refresh.JitterRatio > 1 {
		result.Errors = append(result.Errors, "refresh.jitter_ratio must be between 0 and 1")
	}
	if c.Logging.Level != "debug" && c.Logging.Level != "info" && c.Logging.Level != "warn" && c.Logging.Level != "error" {
		result.Errors = append(result.Errors, "logging.level must be debug, info, warn, or error")
	}
	if c.Logging.Format != "text" && c.Logging.Format != "json" {
		result.Errors = append(result.Errors, "logging.format must be text or json")
	}

	if c.Collectors.FPL != nil {
		result.ActiveCollectors = append(result.ActiveCollectors, "fpl")
		checkFPL(c.Collectors.FPL, &result)
	}
	if c.Collectors.FOFA != nil {
		result.ActiveCollectors = append(result.ActiveCollectors, "fofa")
		checkFOFA(c.Collectors.FOFA, &result)
	}
	if c.Collectors.FreeProxyDB != nil {
		result.ActiveCollectors = append(result.ActiveCollectors, "freeproxydb")
		checkFreeProxyDB(c.Collectors.FreeProxyDB, &result)
	}
	if len(result.ActiveCollectors) == 0 {
		result.Errors = append(result.Errors, "at least one collector must be configured")
	}
	return result
}

func checkFPL(c *FPLConfig, result *CheckResult) {
	if c.RefreshInterval.Duration < MinRefreshInterval {
		result.Errors = append(result.Errors, "collectors.fpl.refresh_interval must be at least 1m")
	}
	if c.MaxResponseBytes.Bytes < 1 || c.MaxResponseBytes.Bytes > MaxFPLMaxResponseBytes {
		result.Errors = append(result.Errors, fmt.Sprintf("collectors.fpl.max_response_bytes must be between 1 and %d", MaxFPLMaxResponseBytes))
	}
	if c.TotalMaxCandidates < 1 || c.TotalMaxCandidates > MaxFPLTotalCandidates {
		result.Errors = append(result.Errors, fmt.Sprintf("collectors.fpl.total_max_candidates must be between 1 and %d", MaxFPLTotalCandidates))
	}
	if c.FetchConcurrency < 1 || c.FetchConcurrency > MaxFPLFetchConcurrency {
		result.Errors = append(result.Errors, fmt.Sprintf("collectors.fpl.fetch_concurrency must be between 1 and %d", MaxFPLFetchConcurrency))
	}
	if len(c.Sources) == 0 || len(c.Sources) > MaxFPLSources {
		result.Errors = append(result.Errors, fmt.Sprintf("collectors.fpl.sources must contain between 1 and %d sources", MaxFPLSources))
	}
	seen := map[string]struct{}{}
	totalBudget := 0
	for i, source := range c.Sources {
		prefix := fmt.Sprintf("collectors.fpl.sources[%d]", i)
		checkSourceName(prefix, source.Name, seen, result)
		if err := validateHTTPURL(source.URL); err != nil {
			result.Errors = append(result.Errors, prefix+".url: "+err.Error())
		}
		if source.Format != FPLFormatURLList && source.Format != FPLFormatHostPort {
			result.Errors = append(result.Errors, prefix+".format must be url_list or host_port")
		}
		if source.Format == FPLFormatHostPort && source.Protocol != "http" && source.Protocol != "socks5" {
			result.Errors = append(result.Errors, prefix+".protocol must be http or socks5")
		}
		if source.Format == FPLFormatURLList && source.Protocol != "" {
			result.Errors = append(result.Errors, prefix+".protocol is only valid for host_port")
		}
		if source.MaxResponseBytes.Bytes < 1 || source.MaxResponseBytes.Bytes > MaxFPLMaxResponseBytes {
			result.Errors = append(result.Errors, prefix+".max_response_bytes is out of range")
		}
		if source.MaxCandidates < 1 || source.MaxCandidates > c.TotalMaxCandidates {
			result.Errors = append(result.Errors, prefix+".max_candidates is out of range")
		} else {
			totalBudget += source.MaxCandidates
		}
	}
	if totalBudget > c.TotalMaxCandidates*2 {
		result.Errors = append(result.Errors, "collectors.fpl source max_candidates sum must not exceed twice total_max_candidates")
	}
}

func checkFOFA(c *FOFAConfig, result *CheckResult) {
	if c.RefreshInterval.Duration < MinRefreshInterval {
		result.Errors = append(result.Errors, "collectors.fofa.refresh_interval must be at least 1m")
	}
	if err := validateHTTPURL(c.BaseURL); err != nil {
		result.Errors = append(result.Errors, "collectors.fofa.base_url: "+err.Error())
	}
	if strings.TrimSpace(c.Key) == "" {
		result.Errors = append(result.Errors, "collectors.fofa.key is required")
	}
	if c.Size < 1 || c.Size > MaxFOFASize {
		result.Errors = append(result.Errors, fmt.Sprintf("collectors.fofa.size must be between 1 and %d", MaxFOFASize))
	}
	if c.TotalMaxCandidates < 1 || c.TotalMaxCandidates > DefaultFOFATotalCandidates {
		result.Errors = append(result.Errors, fmt.Sprintf("collectors.fofa.total_max_candidates must be between 1 and %d", DefaultFOFATotalCandidates))
	}
	if len(c.Queries) == 0 || len(c.Queries) > MaxFOFAQueries {
		result.Errors = append(result.Errors, fmt.Sprintf("collectors.fofa.queries must contain between 1 and %d queries", MaxFOFAQueries))
	}
	seen := map[string]struct{}{}
	for i, query := range c.Queries {
		prefix := fmt.Sprintf("collectors.fofa.queries[%d]", i)
		checkSourceName(prefix, query.Name, seen, result)
		if query.Protocol != "http" && query.Protocol != "socks5" {
			result.Errors = append(result.Errors, prefix+".protocol must be http or socks5")
		}
		if strings.TrimSpace(query.Query) == "" || len(query.Query) > MaxQueryBytes {
			result.Errors = append(result.Errors, prefix+".query is required and must be at most 4096 bytes")
		}
		if strings.TrimSpace(query.Fields) == "" || len(query.Fields) > MaxFieldsBytes {
			result.Errors = append(result.Errors, prefix+".fields is required and must be at most 1024 bytes")
		}
	}
}

func checkFreeProxyDB(c *FreeProxyDBConfig, result *CheckResult) {
	if c.RefreshInterval.Duration < MinRefreshInterval {
		result.Errors = append(result.Errors, "collectors.freeproxydb.refresh_interval must be at least 1m")
	}
	if err := validateHTTPURL(c.BaseURL); err != nil {
		result.Errors = append(result.Errors, "collectors.freeproxydb.base_url: "+err.Error())
	}
	if c.PageSize < 1 || c.PageSize > MaxFreeProxyDBPageSize {
		result.Errors = append(result.Errors, fmt.Sprintf("collectors.freeproxydb.page_size must be between 1 and %d", MaxFreeProxyDBPageSize))
	}
	if c.RequestInterval.Duration < 0 {
		result.Errors = append(result.Errors, "collectors.freeproxydb.request_interval must not be negative")
	}
	if c.MaxCandidates < 1 || c.MaxCandidates > MaxFreeProxyDBCandidates {
		result.Errors = append(result.Errors, fmt.Sprintf("collectors.freeproxydb.max_candidates must be between 1 and %d", MaxFreeProxyDBCandidates))
	}
}

func (c Config) OutputPath() string { return filepath.Join(c.Output.Directory, c.Output.Filename) }
func (c Config) StatePath() string  { return filepath.Join(c.Output.Directory, StateFilename) }

func (c Config) WebPath() string {
	prefix := "/" + strings.Trim(c.Web.PathPrefix, "/")
	if prefix == "/" {
		return "/" + c.Output.Filename
	}
	return prefix + "/" + c.Output.Filename
}

func validateListen(value string) error {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return err
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

func validatePathPrefix(value string) error {
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "?#\\\x00") {
		return fmt.Errorf("must be an absolute URL path without query or fragment")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || strings.Contains(value, "..") {
		return fmt.Errorf("must not contain dot segments")
	}
	return nil
}

func validateFilename(value string) error {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value || strings.ContainsAny(value, "/\\\x00") {
		return fmt.Errorf("must be a plain filename")
	}
	if !strings.HasSuffix(strings.ToLower(value), ".txt") {
		return fmt.Errorf("must end in .txt")
	}
	return nil
}

func validateProxyURL(value string) error {
	u, err := url.Parse(value)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("must use http or https and include a host")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("must not contain path, query, or fragment")
	}
	return nil
}

func validateHTTPURL(value string) error {
	if len(value) == 0 || len(value) > MaxURLBytes {
		return fmt.Errorf("must contain between 1 and %d bytes", MaxURLBytes)
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("must be an absolute http or https URL")
	}
	return nil
}

var sourceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func checkSourceName(prefix, name string, seen map[string]struct{}, result *CheckResult) {
	if !sourceNamePattern.MatchString(name) {
		result.Errors = append(result.Errors, prefix+".name must contain 1-64 letters, digits, dots, underscores, or hyphens")
		return
	}
	if _, exists := seen[name]; exists {
		result.Errors = append(result.Errors, prefix+".name must be unique")
		return
	}
	seen[name] = struct{}{}
}

func DefaultFOFAQueries() []FOFAQueryConfig {
	return []FOFAQueryConfig{
		{Name: "socks5-no-auth", Protocol: "socks5", Query: `protocol=="socks5" && banner="Method:No Authentication"`, Fields: "ip,port,protocol,host"},
		{Name: "http-proxy", Protocol: "http", Query: `banner="Proxy-Authenticate" || banner="Proxy Authentication Required" || banner="Proxy-Agent" || banner="Squid" || banner="tinyproxy" || banner="3proxy"`, Fields: "ip,port,protocol,host"},
	}
}

func DefaultFPLSources() []FPLSourceConfig {
	return []FPLSourceConfig{
		{Name: "proxifly-all", URL: "https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/all/data.txt", Format: FPLFormatURLList, MaxCandidates: 1000},
		{Name: "thespeedx-http", URL: "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/http.txt", Format: FPLFormatHostPort, Protocol: "http", MaxCandidates: 600},
		{Name: "thespeedx-socks5", URL: "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/socks5.txt", Format: FPLFormatHostPort, Protocol: "socks5", MaxCandidates: 400},
		{Name: "clarketm-http", URL: "https://raw.githubusercontent.com/clarketm/proxy-list/master/proxy-list-raw.txt", Format: FPLFormatHostPort, Protocol: "http", MaxCandidates: 600},
		{Name: "proxyscrape-all", URL: "https://raw.githubusercontent.com/ProxyScrape/free-proxy-list/main/proxies/all/data.txt", Format: FPLFormatURLList, MaxCandidates: 1000},
		{Name: "vpslab-http", URL: "https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/http_all.txt", Format: FPLFormatHostPort, Protocol: "http", MaxCandidates: 600},
		{Name: "vpslab-socks5", URL: "https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/socks5_all.txt", Format: FPLFormatHostPort, Protocol: "socks5", MaxCandidates: 600},
		{Name: "roosterkid-http", URL: "https://raw.githubusercontent.com/roosterkid/openproxylist/main/HTTPS_RAW.txt", Format: FPLFormatHostPort, Protocol: "http", MaxCandidates: 600},
		{Name: "roosterkid-socks5", URL: "https://raw.githubusercontent.com/roosterkid/openproxylist/main/SOCKS5_RAW.txt", Format: FPLFormatHostPort, Protocol: "socks5", MaxCandidates: 400},
		{Name: "vakhov-http", URL: "https://raw.githubusercontent.com/vakhov/fresh-proxy-list/master/http.txt", Format: FPLFormatHostPort, Protocol: "http", MaxCandidates: 600},
		{Name: "vakhov-https", URL: "https://raw.githubusercontent.com/vakhov/fresh-proxy-list/master/https.txt", Format: FPLFormatHostPort, Protocol: "http", MaxCandidates: 400},
		{Name: "vakhov-socks5", URL: "https://raw.githubusercontent.com/vakhov/fresh-proxy-list/master/socks5.txt", Format: FPLFormatHostPort, Protocol: "socks5", MaxCandidates: 400},
		{Name: "iplocate-all", URL: "https://raw.githubusercontent.com/iplocate/free-proxy-list/main/all-proxies.txt", Format: FPLFormatURLList, MaxCandidates: 1000},
		{Name: "jetkai-http", URL: "https://raw.githubusercontent.com/jetkai/proxy-list/main/online-proxies/txt/proxies-http.txt", Format: FPLFormatHostPort, Protocol: "http", MaxCandidates: 600},
		{Name: "jetkai-https", URL: "https://raw.githubusercontent.com/jetkai/proxy-list/main/online-proxies/txt/proxies-https.txt", Format: FPLFormatHostPort, Protocol: "http", MaxCandidates: 400},
		{Name: "jetkai-socks5", URL: "https://raw.githubusercontent.com/jetkai/proxy-list/main/online-proxies/txt/proxies-socks5.txt", Format: FPLFormatHostPort, Protocol: "socks5", MaxCandidates: 400},
	}
}
