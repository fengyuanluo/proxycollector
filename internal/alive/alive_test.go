package alive

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestFilterKeepsOnlyReachable(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	aliveAddr := target.Addr().String()

	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := deadListener.Addr().String()
	_ = deadListener.Close()

	urls := []string{
		"http://" + aliveAddr,
		"socks5://" + deadAddr,
		"not-a-proxy-url",
	}
	checker := New(8, time.Second)
	got := checker.Filter(context.Background(), urls)
	if len(got) != 1 || got[0] != urls[0] {
		t.Fatalf("filtered=%v want only %q", got, urls[0])
	}
}

func TestFilterConcurrencyAgrees(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	aliveAddr := target.Addr().String()

	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := deadListener.Addr().String()
	_ = deadListener.Close()

	urls := []string{
		"http://" + aliveAddr,
		"socks5://" + deadAddr,
		"http://" + aliveAddr,
		"socks5://" + deadAddr,
	}
	want := []string{urls[0], urls[2]}
	for _, concurrency := range []int{1, 8} {
		got := New(concurrency, time.Second).Filter(context.Background(), urls)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("concurrency=%d filtered=%v want=%v", concurrency, got, want)
		}
	}
}

func TestFilterUnreachableWithShortTimeoutIsDead(t *testing.T) {
	checker := New(2, 50*time.Millisecond)
	got := checker.Filter(context.Background(), []string{"http://10.255.255.1:80"})
	if len(got) != 0 {
		t.Fatalf("filtered=%v want empty", got)
	}
}

func TestFilterCanceledContextReturnsQuickly(t *testing.T) {
	checker := New(4, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	urls := make([]string, 32)
	for i := range urls {
		urls[i] = "http://10.255.255.1:80"
	}
	start := time.Now()
	got := checker.Filter(ctx, urls)
	if len(got) != 0 {
		t.Fatalf("filtered=%v want empty", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("filter took %v with canceled context", elapsed)
	}
}
