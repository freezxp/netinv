package httpx

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

func TestHealthEndpoints(t *testing.T) {
	h := NewHealthServer("127.0.0.1:18099", slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Start(ctx) }()

	waitFor(t, "http://127.0.0.1:18099/healthz", http.StatusOK)

	if got := status(t, "http://127.0.0.1:18099/readyz"); got != http.StatusServiceUnavailable {
		t.Errorf("readyz before ready = %d, want 503", got)
	}
	h.SetReady(true)
	if got := status(t, "http://127.0.0.1:18099/readyz"); got != http.StatusOK {
		t.Errorf("readyz after ready = %d, want 200", got)
	}
}

func waitFor(t *testing.T, url string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s never returned %d", url, want)
}

func status(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
