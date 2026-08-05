package rolaip

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fengyuanluo/proxycollector/internal/config"
	"github.com/fengyuanluo/proxycollector/internal/fetch"
)

func TestCollectPaginatesAndNormalizesProtocols(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		page := request.URL.Query().Get("page")
		response := listResponse{Pagination: pagination{PageSize: 3, Total: 6, TotalPages: 2}}
		switch page {
		case "1":
			response.Pagination.Page = 1
			response.Data = []proxyRecord{
				{IP: "1.1.1.1", Port: 80, Protocols: []string{"http", "https"}},
				{IP: "2.2.2.2", Port: 1080, Protocols: []string{"socks5", "socks4"}},
				{IP: "3.3.3.3", Port: 1080, Protocols: []string{"socks4"}},
			}
		case "2":
			response.Pagination.Page = 2
			response.Data = []proxyRecord{
				{IP: "1.1.1.1", Port: 80, Protocols: []string{"https"}},
				{IP: "4.4.4.4", Port: 3128, Protocols: []string{"http", "socks5"}},
				{IP: "bad host", Port: 80, Protocols: []string{"http"}},
			}
		default:
			http.Error(writer, "unexpected page", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()

	client, err := fetch.New("", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	collector := New(config.RolaIPConfig{
		BaseURL:         server.URL,
		RefreshInterval: config.Duration{Duration: time.Minute},
		PageSize:        3,
		RequestInterval: config.Duration{},
		MaxCandidates:   10,
	}, client)
	collector.sleep = func(context.Context, time.Duration) error { return nil }

	results := collector.Collect(context.Background())
	if len(results) != 1 {
		t.Fatalf("results=%d", len(results))
	}
	result := results[0]
	got := make([]string, len(result.Proxies))
	for i, proxy := range result.Proxies {
		got[i] = proxy.URL()
	}
	want := []string{
		"http://1.1.1.1:80",
		"socks5://2.2.2.2:1080",
		"http://4.4.4.4:3128",
		"socks5://4.4.4.4:3128",
	}
	if len(got) != len(want) {
		t.Fatalf("proxies=%v report=%+v", got, result.Report)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("proxies=%v want=%v", got, want)
		}
	}
	if result.Report.Error != "" || result.Report.Partial {
		t.Fatalf("report=%+v", result.Report)
	}
	for _, reason := range []string{"unsupported_socks4", "duplicate_proxy", "invalid_proxy"} {
		if result.Report.SkipReasons[reason] != 1 {
			t.Fatalf("skip_reasons=%v", result.Report.SkipReasons)
		}
	}
}

func TestCollectReturnsFirstPageWhenLaterPageFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("page") == "2" {
			http.Error(writer, "upstream failure", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(writer).Encode(listResponse{
			Data:       []proxyRecord{{IP: "1.1.1.1", Port: 80, Protocols: []string{"http"}}},
			Pagination: pagination{Page: 1, PageSize: 1, Total: 2, TotalPages: 2},
		})
	}))
	defer server.Close()

	client, err := fetch.New("", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	collector := New(config.RolaIPConfig{
		BaseURL:         server.URL,
		RefreshInterval: config.Duration{Duration: time.Minute},
		PageSize:        1,
		MaxCandidates:   10,
	}, client)
	collector.sleep = func(context.Context, time.Duration) error { return nil }

	result := collector.Collect(context.Background())[0]
	if len(result.Proxies) != 1 || result.Proxies[0].URL() != "http://1.1.1.1:80" {
		t.Fatalf("proxies=%v", result.Proxies)
	}
	if result.Report.Error != "rolaip page 2 status 502 Bad Gateway" {
		t.Fatalf("report=%+v", result.Report)
	}
	if !result.Report.Partial {
		t.Fatalf("report=%+v", result.Report)
	}
}
