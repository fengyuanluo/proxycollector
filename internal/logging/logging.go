package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/fengyuanluo/proxycollector/internal/config"
)

func New(cfg config.LoggingConfig, output io.Writer) (*slog.Logger, error) {
	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("unsupported log level %q", cfg.Level)
	}
	options := &slog.HandlerOptions{Level: level}
	if cfg.Format == "json" {
		return slog.New(slog.NewJSONHandler(output, options)), nil
	}
	if cfg.Format != "text" {
		return nil, fmt.Errorf("unsupported log format %q", cfg.Format)
	}
	return slog.New(slog.NewTextHandler(output, options)), nil
}
