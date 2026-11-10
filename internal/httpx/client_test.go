package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetBytesRetriesRateLimit(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("<Error><Code>QpsLimitExceeded</Code></Error>"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	c := New(Options{
		Name:       "test",
		BaseURL:    ts.URL,
		Timeout:    2 * time.Second,
		MaxRetries: 3,
	})
	data, err := c.GetBytes(context.Background(), ts.URL+"/result.zip", 1024)
	if err != nil {
		t.Fatalf("GetBytes should retry transient 503: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("unexpected body %q", data)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestGetBytesDoesNotRetry404(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c := New(Options{Name: "test", BaseURL: ts.URL, MaxRetries: 3, Timeout: 2 * time.Second})
	if _, err := c.GetBytes(context.Background(), ts.URL+"/x", 1024); err == nil {
		t.Fatal("expected error for 404")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("404 should not be retried, got %d calls", calls)
	}
}
