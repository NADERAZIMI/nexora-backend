package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRequestIDIsGeneratedAndPreserved(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestIDFromContext(r.Context()) == "" {
			t.Fatal("request ID missing from context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	if first.Header().Get(HeaderRequestID) == "" {
		t.Fatal("generated request ID missing from response")
	}

	second := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(HeaderRequestID, "req-b01-test")
	handler.ServeHTTP(second, request)
	if got := second.Header().Get(HeaderRequestID); got != "req-b01-test" {
		t.Fatalf("request ID = %q", got)
	}
}

func TestRequestIDRejectsUnsafeValues(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(HeaderRequestID, "bad id\nwith newline")
	handler.ServeHTTP(recorder, request)
	if got := recorder.Header().Get(HeaderRequestID); got == "bad id\nwith newline" || got == "" {
		t.Fatalf("unsafe request ID accepted: %q", got)
	}
}

func TestRecoverReturnsJSON500(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "INTERNAL") {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

func TestMetricsRecordRequest(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	handler := metrics.Middleware("test")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/hello", nil))

	count := testutil.ToFloat64(metrics.requests.WithLabelValues("test", http.MethodGet, "/v1/hello", "200"))
	if count != 1 {
		t.Fatalf("request count = %v", count)
	}
}

func TestPanicsAreLoggedAndCounted(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	handler := Chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }),
		RequestID,
		AccessLog(logger),
		metrics.Middleware("test"),
		Recover(logger),
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	count := testutil.ToFloat64(metrics.requests.WithLabelValues("test", http.MethodGet, "/panic", "500"))
	if count != 1 {
		t.Fatalf("panic request count = %v", count)
	}
}
