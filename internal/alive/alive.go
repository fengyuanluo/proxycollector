package alive

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/fengyuanluo/proxycollector/internal/model"
)

// Checker verifies basic TCP connectivity to proxy endpoints.
type Checker struct {
	concurrency int
	timeout     time.Duration
}

func New(concurrency int, timeout time.Duration) *Checker {
	return &Checker{concurrency: concurrency, timeout: timeout}
}

// Filter returns the subset of proxy URLs whose host:port accepts a TCP
// connection. URL parse failures and dial errors are treated as dead.
func (c *Checker) Filter(ctx context.Context, proxyURLs []string) []string {
	if len(proxyURLs) == 0 {
		return nil
	}
	workers := c.concurrency
	if workers < 1 {
		workers = 1
	}
	if workers > len(proxyURLs) {
		workers = len(proxyURLs)
	}
	type outcome struct {
		index int
		alive bool
	}
	jobs := make(chan int)
	results := make(chan outcome, len(proxyURLs))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				results <- outcome{index: index, alive: c.check(ctx, proxyURLs[index])}
			}
		}()
	}
	go func() {
		for index := range proxyURLs {
			select {
			case jobs <- index:
			case <-ctx.Done():
			}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	alive := make([]bool, len(proxyURLs))
	for result := range results {
		alive[result.index] = result.alive
	}
	filtered := make([]string, 0, len(proxyURLs))
	for index, raw := range proxyURLs {
		if alive[index] {
			filtered = append(filtered, raw)
		}
	}
	return filtered
}

func (c *Checker) check(ctx context.Context, raw string) bool {
	proxy, err := model.Parse(raw)
	if err != nil {
		return false
	}
	address := net.JoinHostPort(proxy.Host, strconv.Itoa(proxy.Port))
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
