package gemini

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRetryAfterFromBody(t *testing.T) {
	body := []byte(`{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 8s.","status":"RESOURCE_EXHAUSTED"}}`)
	got := retryAfterFromBody(body)
	if got != 8*time.Second {
		t.Fatalf("expected 8s, got %v", got)
	}
}

func TestRetryAfterFromBody_NoMatch(t *testing.T) {
	body := []byte(`{"error":{"code":403,"message":"Forbidden"}}`)
	if got := retryAfterFromBody(body); got != 0 {
		t.Fatalf("expected 0 (no hint), got %v", got)
	}
}

func TestRetryAfterFromHeader(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "5")
	got := retryAfterFromAll(h, nil)
	if got != 5*time.Second {
		t.Fatalf("expected 5s, got %v", got)
	}
}

func TestRetryAfterPrefersHeader(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "3")
	body := []byte(`{"error":{"message":"reset after 99s"}}`)
	got := retryAfterFromAll(h, body)
	if got != 3*time.Second {
		t.Fatalf("expected header to win (3s), got %v", got)
	}
}

func TestClassify_429Retryable(t *testing.T) {
	body := []byte(`{"error":{"message":"quota will reset after 2s"}}`)
	retry, hint := classify(429, http.Header{}, body)
	if !retry {
		t.Fatal("429 should be retryable")
	}
	if hint != 2*time.Second {
		t.Fatalf("hint expected 2s, got %v", hint)
	}
}

func TestClassify_5xxRetryable(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		retry, _ := classify(code, http.Header{}, nil)
		if !retry {
			t.Fatalf("status %d should be retryable", code)
		}
	}
}

func TestClassify_4xxNotRetryable(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404} {
		retry, _ := classify(code, http.Header{}, nil)
		if retry {
			t.Fatalf("status %d should NOT be retryable", code)
		}
	}
}

func TestBackoffFor_RespectsServerHint(t *testing.T) {
	// First attempt with 8s hint → wait should be at least 8s (with jitter ±25%).
	wait := backoffFor(0, 8*time.Second)
	min, max := 6*time.Second, 10*time.Second
	if wait < min || wait > max {
		t.Fatalf("expected wait in [%v,%v], got %v", min, max, wait)
	}
}

func TestBackoffFor_ExponentialWithoutHint(t *testing.T) {
	// Attempt 0: ~1s, 1: ~2s, 2: ~4s, capped at maxRetryWait
	for attempt, baseSec := range []int{1, 2, 4, 8, 16} {
		wait := backoffFor(attempt, 0)
		base := time.Duration(baseSec) * time.Second
		if base > maxRetryWait {
			base = maxRetryWait
		}
		// jitter ±25%
		min := time.Duration(float64(base) * 0.74)
		max := time.Duration(float64(base) * 1.26)
		if wait < min || wait > max {
			t.Fatalf("attempt %d: expected ~%v ±25%%, got %v", attempt, base, wait)
		}
	}
}

// Live retry test: spin a tiny HTTP server that returns 429 twice then 200.
// We use codeassist.Client.doWithRetry directly to avoid plumbing through
// the full request pipeline.
func TestDoWithRetry_Retries429ThenSucceeds(t *testing.T) {
	t.Setenv("MINI_QUIET", "1") // silence stderr during test

	calls := 0
	srv := startTestServer(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"reset after 1s"}}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	})
	defer srv.Close()

	c := newClient("token")
	build := func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}
	res, err := c.doWithRetry(serverCtx(), build)
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("expected 200 after retries, got %d", res.StatusCode)
	}
	if calls != 3 {
		t.Fatalf("expected exactly 3 attempts, got %d", calls)
	}
}

func TestDoWithRetry_GivesUpAfterMax(t *testing.T) {
	t.Setenv("MINI_QUIET", "1")

	calls := 0
	srv := startTestServer(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"reset after 0s"}}`))
	})
	defer srv.Close()

	c := newClient("token")
	build := func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}
	_, err := c.doWithRetry(serverCtx(), build)
	if err == nil {
		t.Fatal("expected error after max attempts")
	}
	if !strings.Contains(err.Error(), "after") {
		t.Fatalf("expected exhaustion error, got %v", err)
	}
	if calls != maxRetryAttempts {
		t.Fatalf("expected exactly %d attempts, got %d", maxRetryAttempts, calls)
	}
}

func TestDoWithRetry_4xxFailsImmediately(t *testing.T) {
	calls := 0
	srv := startTestServer(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(403)
		_, _ = w.Write([]byte("forbidden"))
	})
	defer srv.Close()

	c := newClient("token")
	build := func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}
	_, err := c.doWithRetry(serverCtx(), build)
	if err == nil {
		t.Fatal("expected 403 to fail without retry")
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt for 4xx, got %d", calls)
	}
}
