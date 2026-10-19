package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nexora-exchange/nexora/libs/go/config"
	"github.com/nexora-exchange/nexora/libs/go/httpx"
	"github.com/nexora-exchange/nexora/libs/go/observability"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	providers, err := observability.New(t.Context(), "hello-service", "test", "")
	if err != nil {
		t.Fatalf("observability.New() error = %v", err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := config.Config{
		ServiceName: "hello-service",
		Environment: "test",
		HTTPAddr:    ":0",
		LogLevel:    "info",
	}
	return New(cfg, logger, providers, "0.1.0")
}

func TestHealthEndpoints(t *testing.T) {
	handler := newTestHandler(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "hello-service") {
			t.Fatalf("%s body = %q", path, recorder.Body.String())
		}
	}
}

func TestHelloEchoesRequestID(t *testing.T) {
	handler := newTestHandler(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/hello", nil)
	request.Header.Set(httpx.HeaderRequestID, "req-b01-test")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get(httpx.HeaderRequestID); got != "req-b01-test" {
		t.Fatalf("response request ID = %q", got)
	}
	var body map[string]string
	if err := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes())).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["request_id"] != "req-b01-test" {
		t.Fatalf("body request_id = %q", body["request_id"])
	}
}

func TestMetricsEndpoint(t *testing.T) {
	handler := newTestHandler(t)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "http_requests_total") {
		t.Fatalf("metrics body missing http_requests_total")
	}
	if !strings.Contains(body, `path="/healthz"`) {
		t.Fatalf("metrics body missing health path label")
	}
}
