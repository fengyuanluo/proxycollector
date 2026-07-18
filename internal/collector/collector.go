package collector

import (
	"context"
	"time"

	"github.com/fengyuanluo/proxycollector/internal/model"
)

type Report struct {
	Collector   string         `json:"collector"`
	Source      string         `json:"source"`
	StartedAt   time.Time      `json:"started_at"`
	FinishedAt  time.Time      `json:"finished_at"`
	Total       int            `json:"total"`
	Imported    int            `json:"imported"`
	Skipped     int            `json:"skipped"`
	SkipReasons map[string]int `json:"skip_reasons,omitempty"`
	Error       string         `json:"error,omitempty"`
	Partial     bool           `json:"partial,omitempty"`
}

func (r *Report) AddSkip(reason string) {
	r.Skipped++
	if r.SkipReasons == nil {
		r.SkipReasons = map[string]int{}
	}
	r.SkipReasons[reason]++
}

type Result struct {
	Source  string
	Proxies []model.Proxy
	Report  Report
}

type Collector interface {
	Name() string
	RefreshInterval() time.Duration
	Sources() []string
	Collect(context.Context) []Result
	Close()
}
