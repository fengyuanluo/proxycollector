package fetch

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	httpClient *http.Client
	transport  *http.Transport
}

func New(proxyURL string, timeout time.Duration) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 8
	transport.IdleConnTimeout = 90 * time.Second

	if proxyURL != "" {
		u, err := ParseProxyURL(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(u)
	}
	return &Client{
		httpClient: &http.Client{Transport: transport, Timeout: timeout},
		transport:  transport,
	}, nil
}

func ParseProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse fetch proxy URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("fetch proxy URL must use http or https and include a host")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("fetch proxy URL must not contain path, query, or fragment")
	}
	return u, nil
}

func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.httpClient.Do(req)
}

func (c *Client) Close() {
	c.transport.CloseIdleConnections()
}

func Error(err error) string {
	var urlError *url.Error
	if errors.As(err, &urlError) && urlError.Err != nil {
		return urlError.Err.Error()
	}
	return err.Error()
}
