package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fengyuanluo/proxycollector/internal/model"
)

const (
	currentVersion = 1
	maxStateBytes  = 64 << 20
	maxProxies     = 100000
)

type Source struct {
	UpdatedAt time.Time `json:"updated_at"`
	Proxies   []string  `json:"proxies"`
}

type persistedState struct {
	Version         int               `json:"version"`
	Sources         map[string]Source `json:"sources"`
	RecoveryPending []string          `json:"recovery_pending,omitempty"`
}

type UpdateResult struct {
	Accepted        bool
	Published       bool
	ProxyCount      int
	RecoveryPending int
	DeadCount       int
}

// AliveFilter keeps only proxy URLs that should be published. It is applied
// to the aggregate proxy set right before the TXT file is written.
type AliveFilter func(context.Context, []string) []string

type Store struct {
	mu         sync.Mutex
	statePath  string
	outputPath string
	expected   map[string]struct{}
	alive      AliveFilter
	state      persistedState
}

func Open(statePath, outputPath string, expectedSources []string, filters ...AliveFilter) (*Store, error) {
	if filepath.Dir(statePath) != filepath.Dir(outputPath) {
		return nil, fmt.Errorf("state and output files must share a directory")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}
	store := &Store{
		statePath:  statePath,
		outputPath: outputPath,
		expected:   make(map[string]struct{}, len(expectedSources)),
		state:      persistedState{Version: currentVersion, Sources: map[string]Source{}},
	}
	for _, source := range expectedSources {
		if source == "" {
			return nil, fmt.Errorf("expected source key is empty")
		}
		if _, duplicate := store.expected[source]; duplicate {
			return nil, fmt.Errorf("duplicate expected source key %q", source)
		}
		store.expected[source] = struct{}{}
	}
	if len(filters) > 0 {
		store.alive = filters[0]
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) load() error {
	outputExists := fileExists(s.outputPath)
	data, err := readLimitedFile(s.statePath, maxStateBytes)
	if errors.Is(err, os.ErrNotExist) {
		if outputExists {
			s.enterRecovery()
			return s.saveStateLocked()
		}
		return nil
	}
	if err != nil {
		if outputExists {
			if quarantineErr := quarantine(s.statePath); quarantineErr != nil {
				return fmt.Errorf("quarantine unreadable state: %w", quarantineErr)
			}
			s.enterRecovery()
			return s.saveStateLocked()
		}
		return fmt.Errorf("load state: %w", err)
	}
	var loaded persistedState
	if err := json.Unmarshal(data, &loaded); err != nil || validateState(loaded) != nil {
		if outputExists {
			if quarantineErr := quarantine(s.statePath); quarantineErr != nil {
				return fmt.Errorf("quarantine invalid state: %w", quarantineErr)
			}
			s.enterRecovery()
			return s.saveStateLocked()
		}
		if err != nil {
			return fmt.Errorf("decode state: %w", err)
		}
		return validateState(loaded)
	}
	s.state = loaded
	changed := s.pruneUnknownLocked()
	if len(s.state.RecoveryPending) == 0 && len(s.state.Sources) == 0 && outputExists {
		s.enterRecovery()
		changed = true
	}
	if changed {
		if err := s.saveStateLocked(); err != nil {
			return err
		}
	}
	if len(s.state.RecoveryPending) > 0 {
		return nil
	}
	_, err = s.publishLocked(context.Background())
	return err
}

func (s *Store) enterRecovery() {
	s.state = persistedState{Version: currentVersion, Sources: map[string]Source{}}
	s.state.RecoveryPending = make([]string, 0, len(s.expected))
	for source := range s.expected {
		s.state.RecoveryPending = append(s.state.RecoveryPending, source)
	}
	sort.Strings(s.state.RecoveryPending)
}

func (s *Store) pruneUnknownLocked() bool {
	changed := false
	for source := range s.state.Sources {
		if _, expected := s.expected[source]; !expected {
			delete(s.state.Sources, source)
			changed = true
		}
	}
	pending := s.state.RecoveryPending[:0]
	for _, source := range s.state.RecoveryPending {
		if _, expected := s.expected[source]; expected {
			pending = append(pending, source)
		} else {
			changed = true
		}
	}
	s.state.RecoveryPending = pending
	return changed
}

func (s *Store) Update(ctx context.Context, source string, proxies []model.Proxy, complete bool) (UpdateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, expected := s.expected[source]; !expected {
		return UpdateResult{}, fmt.Errorf("unexpected source %q", source)
	}
	urls, err := canonicalURLs(proxies)
	if err != nil {
		return UpdateResult{}, err
	}
	recovering := len(s.state.RecoveryPending) > 0
	if !recovering && len(urls) == 0 {
		return UpdateResult{ProxyCount: len(s.aggregateLocked())}, nil
	}
	if len(urls) > 0 || recovering && complete {
		s.state.Sources[source] = Source{UpdatedAt: time.Now().UTC(), Proxies: urls}
	}
	if recovering && complete {
		s.state.RecoveryPending = removeString(s.state.RecoveryPending, source)
	}
	if err := s.saveStateLocked(); err != nil {
		return UpdateResult{}, err
	}
	result := UpdateResult{Accepted: len(urls) > 0 || recovering && complete, RecoveryPending: len(s.state.RecoveryPending)}
	if len(s.state.RecoveryPending) > 0 {
		return result, nil
	}
	return s.publishLocked(ctx)
}

func (s *Store) aggregateLocked() []string {
	seen := make(map[string]struct{})
	for _, source := range s.state.Sources {
		for _, proxyURL := range source.Proxies {
			seen[proxyURL] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for proxyURL := range seen {
		result = append(result, proxyURL)
	}
	sort.Strings(result)
	return result
}

func (s *Store) saveStateLocked() error {
	s.state.Version = currentVersion
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxStateBytes {
		return fmt.Errorf("state exceeds %d bytes", maxStateBytes)
	}
	return writeAtomic(s.statePath, data, 0o600)
}

func (s *Store) publishLocked(ctx context.Context) (UpdateResult, error) {
	proxies := s.aggregateLocked()
	total := len(proxies)
	dead := 0
	if s.alive != nil {
		proxies = s.alive(ctx, proxies)
		dead = total - len(proxies)
	}
	if len(proxies) == 0 {
		if s.alive != nil {
			// Keep the last published list when validation removes everything.
			return UpdateResult{DeadCount: dead}, nil
		}
		if err := os.Remove(s.outputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return UpdateResult{}, fmt.Errorf("remove empty output: %w", err)
		}
		return UpdateResult{}, nil
	}
	var output strings.Builder
	for _, proxyURL := range proxies {
		output.WriteString(proxyURL)
		output.WriteByte('\n')
	}
	if err := writeAtomic(s.outputPath, []byte(output.String()), 0o644); err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Published: true, ProxyCount: len(proxies), DeadCount: dead}, nil
}

func canonicalURLs(proxies []model.Proxy) ([]string, error) {
	if len(proxies) > maxProxies {
		return nil, fmt.Errorf("source contains more than %d proxies", maxProxies)
	}
	seen := make(map[string]struct{}, len(proxies))
	for _, proxy := range proxies {
		if err := proxy.Validate(); err != nil {
			return nil, err
		}
		seen[proxy.URL()] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for proxyURL := range seen {
		result = append(result, proxyURL)
	}
	sort.Strings(result)
	return result, nil
}

func validateState(state persistedState) error {
	if state.Version != currentVersion {
		return fmt.Errorf("unsupported state version %d", state.Version)
	}
	if state.Sources == nil {
		return fmt.Errorf("state sources must not be null")
	}
	total := 0
	for key, source := range state.Sources {
		if key == "" {
			return fmt.Errorf("state source key is empty")
		}
		for _, proxyURL := range source.Proxies {
			proxy, err := model.Parse(proxyURL)
			if err != nil || proxy.URL() != proxyURL {
				return fmt.Errorf("state source %q contains non-canonical proxy URL", key)
			}
			total++
			if total > maxProxies {
				return fmt.Errorf("state contains more than %d proxies", maxProxies)
			}
		}
	}
	seenPending := map[string]struct{}{}
	for _, source := range state.RecoveryPending {
		if source == "" {
			return fmt.Errorf("state recovery source is empty")
		}
		if _, duplicate := seenPending[source]; duplicate {
			return fmt.Errorf("state recovery source %q is duplicated", source)
		}
		seenPending[source] = struct{}{}
	}
	return nil
}

func writeAtomic(filename string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(filename)
	temporary, err := os.CreateTemp(directory, ".proxycollector-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, bytes.NewReader(data)); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filename); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func readLimitedFile(filename string, limit int64) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
}

func quarantine(filename string) error {
	timestamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	return os.Rename(filename, filename+".corrupt-"+timestamp)
}

func fileExists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}

func removeString(values []string, target string) []string {
	for i, value := range values {
		if value == target {
			return append(values[:i], values[i+1:]...)
		}
	}
	return values
}
