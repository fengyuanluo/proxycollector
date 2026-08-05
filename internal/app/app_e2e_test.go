package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServeCollectsThroughExternalProxyAndHostsSortedTXT(t *testing.T) {
	var mu sync.Mutex
	requests := map[string]int{}
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests[request.URL.Host]++
		mu.Unlock()
		switch request.URL.Host {
		case "fpl.test":
			fmt.Fprint(writer, "http://2.2.2.2:80\nsocks5://user:pass@1.1.1.1:1080\nvmess://ignored\nhttp://2.2.2.2:80\n")
		case "fofa.test":
			_ = json.NewEncoder(writer).Encode(map[string]any{"error": false, "results": [][]any{{"2.2.2.2", "80"}}})
		case "free.test":
			_ = json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{
				"total_count": 3,
				"data": []map[string]any{
					{"ip": "2.2.2.2", "port": 80, "protocol": "http"},
					{"ip": "2001:db8::1", "port": 1080, "protocol": "socks5"},
					{"protocol": "vless", "connect_string": "vless://ignored"},
				},
			}})
		case "rola.test":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"data": []map[string]any{
					{"ip": "3.3.3.3", "port": 8080, "protocols": []string{"https", "socks5"}},
					{"ip": "4.4.4.4", "port": 1080, "protocols": []string{"socks4"}},
				},
				"pagination": map[string]any{"page": 1, "pageSize": 500, "total": 2, "totalPages": 1},
			})
		default:
			http.Error(writer, "unexpected target", http.StatusBadGateway)
		}
	}))
	defer proxy.Close()

	directory := t.TempDir()
	port := freePort(t)
	configPath := filepath.Join(directory, "config.yaml")
	configText := fmt.Sprintf(`
web:
  listen: "127.0.0.1:%d"
  path_prefix: /lists
output:
  directory: %q
  filename: collected.txt
fetch:
  proxy_url: %q
  timeout: 5s
refresh:
  jitter_ratio: 0.1
logging:
  level: info
  format: text
collectors:
  fpl:
    refresh_interval: 1m
    total_max_candidates: 10
    sources:
      - name: test
        url: http://fpl.test/list.txt
        format: url_list
        max_candidates: 10
  fofa:
    base_url: http://fofa.test
    key: test-key
    size: 10
    total_max_candidates: 10
    refresh_interval: 1m
    queries:
      - name: test
        protocol: http
        query: test
        fields: ip,port
  freeproxydb:
    base_url: http://free.test
    refresh_interval: 1m
    page_size: 100
    request_interval: 0s
    max_candidates: 10
  rolaip:
    base_url: http://rola.test
    refresh_interval: 1m
    page_size: 500
    request_interval: 1ms
    max_candidates: 10
`, port, directory, proxy.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- Run(ctx, []string{"serve", "-c", configPath}, io.Discard, io.Discard) }()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/lists/collected.txt", port)
	want := strings.Join([]string{
		"http://2.2.2.2:80",
		"http://3.3.3.3:8080",
		"socks5://3.3.3.3:8080",
		"socks5://[2001:db8::1]:1080",
		"socks5://user:pass@1.1.1.1:1080",
	}, "\n") + "\n"
	waitForBody(t, endpoint, want)

	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/other", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("other path status=%d", response.StatusCode)
	}
	request, _ := http.NewRequest(http.MethodPost, endpoint, nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status=%d allow=%q", response.StatusCode, response.Header.Get("Allow"))
	}

	mu.Lock()
	hosts := make([]string, 0, len(requests))
	for host, count := range requests {
		if count == 0 {
			t.Fatalf("no requests for %s", host)
		}
		hosts = append(hosts, host)
	}
	mu.Unlock()
	sort.Strings(hosts)
	if got := strings.Join(hosts, ","); got != "fofa.test,fpl.test,free.test,rola.test" {
		t.Fatalf("proxied target hosts=%q", got)
	}
	assertOutput(t, filepath.Join(directory, "collected.txt"), want)

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exit code=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func waitForBody(t *testing.T, endpoint, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last := "no response"
	for time.Now().Before(deadline) {
		response, err := http.Get(endpoint)
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			last = fmt.Sprintf("status=%d body=%q", response.StatusCode, body)
			if response.StatusCode == http.StatusOK && string(body) == want {
				return
			}
		} else {
			last = err.Error()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("endpoint %s did not return expected body; last=%s", endpoint, last)
}

func assertOutput(t *testing.T, filename, want string) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("output=%q want=%q", data, want)
	}
}
