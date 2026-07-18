package model

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	ProtocolHTTP   = "http"
	ProtocolSOCKS5 = "socks5"

	MaxHostBytes       = 255
	MaxCredentialBytes = 255
)

type Proxy struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func New(protocol, host string, port int, username, password string) (Proxy, error) {
	p := Proxy{
		Protocol: normalizeProtocol(protocol),
		Host:     normalizeHost(host),
		Port:     port,
		Username: username,
		Password: password,
	}
	if err := p.Validate(); err != nil {
		return Proxy{}, err
	}
	return p, nil
}

func Parse(raw string) (Proxy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Proxy{}, fmt.Errorf("proxy URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Proxy{}, fmt.Errorf("parse proxy URL: %w", err)
	}
	if u.Opaque != "" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return Proxy{}, fmt.Errorf("proxy URL must contain only scheme, credentials, host, and port")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return Proxy{}, fmt.Errorf("proxy URL requires a valid port")
	}
	username := ""
	password := ""
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	return New(u.Scheme, u.Hostname(), port, username, password)
}

func (p Proxy) Validate() error {
	if p.Protocol != ProtocolHTTP && p.Protocol != ProtocolSOCKS5 {
		return fmt.Errorf("unsupported proxy protocol %q", p.Protocol)
	}
	if p.Host == "" || len(p.Host) > MaxHostBytes || strings.ContainsAny(p.Host, "\x00\r\n\t /@") {
		return fmt.Errorf("invalid proxy host")
	}
	if ip := net.ParseIP(p.Host); ip == nil && !validHostname(p.Host) {
		return fmt.Errorf("invalid proxy host")
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("invalid proxy port")
	}
	if len(p.Username) > MaxCredentialBytes || len(p.Password) > MaxCredentialBytes {
		return fmt.Errorf("proxy credentials exceed %d bytes", MaxCredentialBytes)
	}
	return nil
}

func validHostname(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-') {
				return false
			}
		}
	}
	return true
}

func (p Proxy) URL() string {
	u := url.URL{Scheme: p.Protocol, Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port))}
	if p.Username != "" || p.Password != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u.String()
}

func normalizeProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "http", "https":
		return ProtocolHTTP
	case "socks", "socks5", "socks5h":
		return ProtocolSOCKS5
	default:
		return strings.ToLower(strings.TrimSpace(protocol))
	}
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return strings.ToLower(host)
}
