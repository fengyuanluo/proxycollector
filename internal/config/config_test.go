package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStrictConfigAndDefaults(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	data := `
output:
  directory: ./out
collectors:
  fpl:
    sources:
      - name: local
        url: http://127.0.0.1/list.txt
        format: host_port
        protocol: http
`
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if result := cfg.Check(); !result.OK() {
		t.Fatalf("check errors=%v", result.Errors)
	}
	if cfg.Web.Listen != "0.0.0.0:27298" || cfg.WebPath() != "/proxies.txt" || cfg.Output.Filename != "proxies.txt" {
		t.Fatalf("defaults=%+v web_path=%q", cfg, cfg.WebPath())
	}
	if cfg.Collectors.FPL.Sources[0].MaxCandidates != DefaultFPLSourceCandidates {
		t.Fatalf("source defaults=%+v", cfg.Collectors.FPL.Sources[0])
	}
	if !cfg.Collectors.RolaIP.IsEnabled() || cfg.Collectors.RolaIP.BaseURL != DefaultRolaIPBaseURL || cfg.Collectors.RolaIP.PageSize != DefaultRolaIPPageSize {
		t.Fatalf("rolaip defaults=%+v", cfg.Collectors.RolaIP)
	}
}

func TestRolaIPCanBeDisabled(t *testing.T) {
	disabled := false
	cfg := Config{Collectors: CollectorsConfig{RolaIP: RolaIPConfig{Enabled: &disabled}}}
	cfg.ApplyDefaults()
	result := cfg.Check()
	if result.OK() || !strings.Contains(strings.Join(result.Errors, "\n"), "at least one collector") {
		t.Fatalf("result=%+v", result)
	}
}

func TestRolaIPRejectsInvalidSettings(t *testing.T) {
	cfg := Config{Collectors: CollectorsConfig{RolaIP: RolaIPConfig{
		BaseURL:  "file:///tmp/proxies.json",
		PageSize: MaxRolaIPPageSize + 1,
	}}}
	cfg.ApplyDefaults()
	result := cfg.Check()
	joined := strings.Join(result.Errors, "\n")
	if !strings.Contains(joined, "collectors.rolaip.base_url") || !strings.Contains(joined, "collectors.rolaip.page_size") {
		t.Fatalf("errors=%v", result.Errors)
	}
}

func TestLoadRejectsRemovedAIOPROXYKeys(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(filename, []byte("server:\n  listen: 127.0.0.1:1080\ncollectors:\n  fpl: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(filename)
	if err == nil || !strings.Contains(err.Error(), "field server not found") {
		t.Fatalf("error=%v", err)
	}
}

func TestCheckRejectsUnsafeOutputAndFetchProxy(t *testing.T) {
	cfg := Config{
		Output:     OutputConfig{Directory: "./data", Filename: "../secret.txt"},
		Fetch:      FetchConfig{ProxyURL: "socks5://127.0.0.1:1080"},
		Collectors: CollectorsConfig{FPL: &FPLConfig{}},
	}
	cfg.ApplyDefaults()
	result := cfg.Check()
	joined := strings.Join(result.Errors, "\n")
	if !strings.Contains(joined, "output.filename") || !strings.Contains(joined, "fetch.proxy_url") {
		t.Fatalf("errors=%v", result.Errors)
	}
}
