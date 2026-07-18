package fetch

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfiguredProxyFailureNeverFallsBackToDirect(t *testing.T) {
	var originRequests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		originRequests.Add(1)
	}))
	defer origin.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddress := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	client, err := New(proxyAddress, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL, nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("request unexpectedly succeeded through unavailable proxy")
	}
	if got := originRequests.Load(); got != 0 {
		t.Fatalf("direct fallback reached origin %d times", got)
	}
}
