package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Server struct {
	listen     string
	path       string
	outputPath string
	logger     *slog.Logger
	server     *http.Server
	listener   net.Listener
}

func New(listen, path, outputPath string, logger *slog.Logger) *Server {
	return &Server{listen: listen, path: path, outputPath: outputPath, logger: logger}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.path, s.serveList)
	s.server = &http.Server{
		Addr:              s.listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", s.listen)
	if err != nil {
		return err
	}
	s.listener = listener
	go func() {
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("web server failed", "error", err)
		}
	}()
	return nil
}

func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) Close(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *Server) serveList(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	file, err := os.Open(s.outputPath)
	if errors.Is(err, os.ErrNotExist) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.Error(writer, "proxy list not ready", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		s.logger.Error("open proxy list failed", "error", err)
		http.Error(writer, "internal server error", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		s.logger.Error("stat proxy list failed", "error", err)
		http.Error(writer, "internal server error", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(writer, request, filepath.Base(s.outputPath), info.ModTime(), file)
}

func ValidatePath(value string) error {
	if value == "" || value[0] != '/' {
		return fmt.Errorf("web path must be absolute")
	}
	return nil
}
