package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestListHandlerReadinessAndMethods(t *testing.T) {
	output := filepath.Join(t.TempDir(), "proxies.txt")
	server := New("127.0.0.1:0", "/proxies.txt", output, slog.New(slog.NewTextHandler(io.Discard, nil)))

	request := httptest.NewRequest(http.MethodGet, "/proxies.txt", nil)
	recorder := httptest.NewRecorder()
	server.serveList(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("not-ready status=%d headers=%v", recorder.Code, recorder.Header())
	}

	if err := os.WriteFile(output, []byte("http://127.0.0.1:80\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodHead, "/proxies.txt", nil)
	recorder = httptest.NewRecorder()
	server.serveList(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 || recorder.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("HEAD status=%d body=%q headers=%v", recorder.Code, recorder.Body.String(), recorder.Header())
	}

	request = httptest.NewRequest(http.MethodPost, "/proxies.txt", nil)
	recorder = httptest.NewRecorder()
	server.serveList(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status=%d allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}
}
