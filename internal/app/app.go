package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fengyuanluo/proxycollector/internal/collector"
	"github.com/fengyuanluo/proxycollector/internal/collectors/fofa"
	"github.com/fengyuanluo/proxycollector/internal/collectors/fpl"
	"github.com/fengyuanluo/proxycollector/internal/collectors/freeproxydb"
	"github.com/fengyuanluo/proxycollector/internal/collectors/rolaip"
	"github.com/fengyuanluo/proxycollector/internal/config"
	"github.com/fengyuanluo/proxycollector/internal/fetch"
	"github.com/fengyuanluo/proxycollector/internal/logging"
	"github.com/fengyuanluo/proxycollector/internal/state"
	"github.com/fengyuanluo/proxycollector/internal/web"
)

func Run(parent context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "serve":
		return runServe(parent, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage:\n  proxycollector check -c config.yaml\n  proxycollector serve -c config.yaml")
}

func configPath(args []string) (string, bool) {
	set := flag.NewFlagSet("config", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var path string
	set.StringVar(&path, "c", "", "config file")
	set.StringVar(&path, "config", "", "config file")
	if err := set.Parse(args); err != nil || path == "" || set.NArg() != 0 {
		return "", false
	}
	return path, true
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	path, ok := configPath(args)
	if !ok {
		fmt.Fprintln(stderr, "missing -c config.yaml")
		return 2
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "ERROR:", err)
		return 1
	}
	result := cfg.Check()
	fmt.Fprintln(stdout, "ProxyCollector config check")
	fmt.Fprintln(stdout, "active collectors:", strings.Join(result.ActiveCollectors, ", "))
	for _, warning := range result.Warnings {
		fmt.Fprintln(stdout, "WARNING:", warning)
	}
	for _, checkErr := range result.Errors {
		fmt.Fprintln(stdout, "ERROR:", checkErr)
	}
	if !result.OK() {
		return 1
	}
	fmt.Fprintln(stdout, "result: ok")
	return 0
}

func runServe(parent context.Context, args []string, stdout, stderr io.Writer) int {
	path, ok := configPath(args)
	if !ok {
		fmt.Fprintln(stderr, "missing -c config.yaml")
		return 2
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "load config:", err)
		return 1
	}
	check := cfg.Check()
	if !check.OK() {
		for _, checkErr := range check.Errors {
			fmt.Fprintln(stderr, "ERROR:", checkErr)
		}
		return 1
	}
	logger, err := logging.New(cfg.Logging, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "logging:", err)
		return 1
	}
	slog.SetDefault(logger)
	client, err := fetch.New(cfg.Fetch.ProxyURL, cfg.Fetch.Timeout.Duration)
	if err != nil {
		logger.Error("create fetch client failed", "error", err)
		return 1
	}
	defer client.Close()
	collectors := buildCollectors(cfg, client)
	for _, item := range collectors {
		defer item.Close()
	}

	expected := sourceKeys(collectors)
	store, err := state.Open(cfg.StatePath(), cfg.OutputPath(), expected)
	if err != nil {
		logger.Error("initialize state failed", "error", err)
		return 1
	}
	webServer := web.New(cfg.Web.Listen, cfg.WebPath(), cfg.OutputPath(), logger)
	if err := webServer.Start(); err != nil {
		logger.Error("web start failed", "error", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var wait sync.WaitGroup
	for _, item := range collectors {
		current := item
		wait.Add(1)
		go func() {
			defer wait.Done()
			collectionLoop(ctx, current, cfg.Refresh.JitterRatio, store, logger)
		}()
	}
	fmt.Fprintf(stdout, "ProxyCollector serving web=%s path=%s output=%s\n", webServer.Addr(), cfg.WebPath(), cfg.OutputPath())
	logger.Info("proxycollector started", "web", webServer.Addr(), "path", cfg.WebPath(), "collectors", len(collectors))
	<-ctx.Done()
	cancel()
	wait.Wait()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := webServer.Close(shutdownCtx); err != nil {
		logger.Error("web shutdown failed", "error", err)
		return 1
	}
	logger.Info("proxycollector stopped")
	return 0
}

func buildCollectors(cfg *config.Config, client *fetch.Client) []collector.Collector {
	items := make([]collector.Collector, 0, 4)
	if cfg.Collectors.FPL != nil {
		items = append(items, fpl.New(*cfg.Collectors.FPL, client))
	}
	if cfg.Collectors.FOFA != nil {
		items = append(items, fofa.New(*cfg.Collectors.FOFA, client))
	}
	if cfg.Collectors.FreeProxyDB != nil {
		items = append(items, freeproxydb.New(*cfg.Collectors.FreeProxyDB, client))
	}
	if cfg.Collectors.RolaIP.IsEnabled() {
		items = append(items, rolaip.New(cfg.Collectors.RolaIP, client))
	}
	return items
}

func sourceKeys(items []collector.Collector) []string {
	keys := make([]string, 0)
	for _, item := range items {
		for _, source := range item.Sources() {
			keys = append(keys, item.Name()+":"+source)
		}
	}
	return keys
}

func collectionLoop(ctx context.Context, item collector.Collector, jitter float64, store *state.Store, logger *slog.Logger) {
	for {
		collectOnce(ctx, item, store, logger)
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(nextDelay(item.RefreshInterval(), jitter, rand.Float64()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func collectOnce(ctx context.Context, item collector.Collector, store *state.Store, logger *slog.Logger) {
	logger.Info("collector refresh started", "collector", item.Name())
	results := item.Collect(ctx)
	if ctx.Err() != nil {
		logger.Info("collector refresh canceled", "collector", item.Name())
		return
	}
	for _, result := range results {
		key := item.Name() + ":" + result.Source
		complete := result.Report.Error == "" && !result.Report.Partial
		update, err := store.Update(key, result.Proxies, complete)
		if err != nil {
			logger.Error("store collector result failed", "collector", item.Name(), "source", result.Source, "error", err)
			continue
		}
		attributes := []any{
			"collector", item.Name(), "source", result.Source,
			"total", result.Report.Total, "imported", len(result.Proxies),
			"skipped", result.Report.Skipped, "partial", result.Report.Partial,
			"accepted", update.Accepted, "published", update.Published,
			"proxy_count", update.ProxyCount, "recovery_pending", update.RecoveryPending,
		}
		if result.Report.Error != "" {
			attributes = append(attributes, "error", result.Report.Error)
			logger.Warn("collector source refresh finished", attributes...)
		} else {
			logger.Info("collector source refresh finished", attributes...)
		}
	}
}

func nextDelay(interval time.Duration, jitter, random float64) time.Duration {
	delay := interval + time.Duration((random*2-1)*jitter*float64(interval))
	if delay < config.MinRefreshInterval {
		return config.MinRefreshInterval
	}
	return delay
}
