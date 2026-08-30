package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fengyuanluo/proxycollector/internal/model"
)

func mustProxy(t *testing.T, raw string) model.Proxy {
	t.Helper()
	proxy, err := model.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return proxy
}

func TestSourceReplacementRetentionDedupeAndPrune(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, ".proxycollector-state.json")
	outputPath := filepath.Join(directory, "proxies.txt")
	store, err := Open(statePath, outputPath, []string{"fpl:a", "fofa:b"})
	if err != nil {
		t.Fatal(err)
	}
	if update, err := store.Update(context.Background(), "fpl:a", []model.Proxy{
		mustProxy(t, "http://2.2.2.2:80"),
		mustProxy(t, "http://1.1.1.1:80"),
	}, true); err != nil || !update.Published || update.ProxyCount != 2 {
		t.Fatalf("first update=%+v err=%v", update, err)
	}
	if _, err := store.Update(context.Background(), "fofa:b", []model.Proxy{
		mustProxy(t, "http://1.1.1.1:80"),
		mustProxy(t, "socks5://3.3.3.3:1080"),
	}, false); err != nil {
		t.Fatal(err)
	}
	if update, err := store.Update(context.Background(), "fpl:a", nil, true); err != nil || update.Accepted {
		t.Fatalf("empty update=%+v err=%v", update, err)
	}
	assertFile(t, outputPath, "http://1.1.1.1:80\nhttp://2.2.2.2:80\nsocks5://3.3.3.3:1080\n")

	if _, err := Open(statePath, outputPath, []string{"fofa:b"}); err != nil {
		t.Fatal(err)
	}
	assertFile(t, outputPath, "http://1.1.1.1:80\nsocks5://3.3.3.3:1080\n")
}

func TestCorruptStateKeepsOldOutputUntilEverySourceRecovers(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, ".proxycollector-state.json")
	outputPath := filepath.Join(directory, "proxies.txt")
	if err := os.WriteFile(statePath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("http://old.example:80\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Open(statePath, outputPath, []string{"fpl:a", "fofa:b"})
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, outputPath, "http://old.example:80\n")
	first, err := store.Update(context.Background(), "fpl:a", []model.Proxy{mustProxy(t, "http://new.example:80")}, true)
	if err != nil || first.Published || first.RecoveryPending != 1 {
		t.Fatalf("first recovery=%+v err=%v", first, err)
	}
	assertFile(t, outputPath, "http://old.example:80\n")
	second, err := store.Update(context.Background(), "fofa:b", nil, true)
	if err != nil || !second.Published || second.RecoveryPending != 0 {
		t.Fatalf("second recovery=%+v err=%v", second, err)
	}
	assertFile(t, outputPath, "http://new.example:80\n")
	matches, err := filepath.Glob(statePath + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("quarantine matches=%v err=%v", matches, err)
	}
}

func TestAliveFilterPublishesOnlySurvivors(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, ".proxycollector-state.json")
	outputPath := filepath.Join(directory, "proxies.txt")
	keep := func(_ context.Context, proxyURLs []string) []string {
		filtered := make([]string, 0, len(proxyURLs))
		for _, raw := range proxyURLs {
			if strings.Contains(raw, "2.2.2.2") {
				filtered = append(filtered, raw)
			}
		}
		return filtered
	}
	store, err := Open(statePath, outputPath, []string{"fpl:a"}, keep)
	if err != nil {
		t.Fatal(err)
	}
	update, err := store.Update(context.Background(), "fpl:a", []model.Proxy{
		mustProxy(t, "http://1.1.1.1:80"),
		mustProxy(t, "http://2.2.2.2:80"),
		mustProxy(t, "socks5://2.2.2.2:1080"),
	}, true)
	if err != nil || !update.Published || update.ProxyCount != 2 || update.DeadCount != 1 {
		t.Fatalf("update=%+v err=%v", update, err)
	}
	assertFile(t, outputPath, "http://2.2.2.2:80\nsocks5://2.2.2.2:1080\n")
}

func TestAliveFilterAllDeadKeepsLastOutput(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, ".proxycollector-state.json")
	outputPath := filepath.Join(directory, "proxies.txt")
	if err := os.WriteFile(outputPath, []byte("http://old.example:80\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dropAll := func(context.Context, []string) []string { return nil }
	store, err := Open(statePath, outputPath, []string{"fpl:a"}, dropAll)
	if err != nil {
		t.Fatal(err)
	}
	update, err := store.Update(context.Background(), "fpl:a", []model.Proxy{
		mustProxy(t, "http://new.example:80"),
	}, true)
	if err != nil || update.Published || update.DeadCount != 1 {
		t.Fatalf("update=%+v err=%v", update, err)
	}
	assertFile(t, outputPath, "http://old.example:80\n")
}

func TestAliveFilterAllDeadWithoutOutputCreatesNothing(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, ".proxycollector-state.json")
	outputPath := filepath.Join(directory, "proxies.txt")
	dropAll := func(context.Context, []string) []string { return nil }
	store, err := Open(statePath, outputPath, []string{"fpl:a"}, dropAll)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(context.Background(), "fpl:a", []model.Proxy{
		mustProxy(t, "http://new.example:80"),
	}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("output should not exist, stat err=%v", err)
	}
}

func assertFile(t *testing.T, filename, want string) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if got != want {
		t.Fatalf("file=%q want=%q", got, want)
	}
	if strings.Contains(got, "\r") {
		t.Fatalf("file contains CR characters: %q", got)
	}
}
