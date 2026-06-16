package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"
)

// Retry policy for cloudcode-pa requests.
//
// Why this lives here, not in the agent: 429 / 5xx / transient network errors
// are wire-level concerns. The agent should never see them — it should see
// either a successful response or a fatal error after retries are exhausted.
//
// What we retry:
//   - HTTP 429 (RESOURCE_EXHAUSTED) — parses the "reset after Ns" hint from
//     the JSON body when present; otherwise exponential backoff with jitter.
//   - HTTP 5xx — exponential backoff with jitter.
//   - Network errors (connection refused, EOF, timeout) — exponential backoff.
//   - Context cancellation/deadline → not retried, propagated immediately.
//
// What we DO NOT retry:
//   - 4xx other than 429 (auth, malformed request) — these will not succeed
//     by retrying.
//   - Mid-stream failures (we only retry before any body is read).

// Defaults — env vars override at runtime.
//   GEMINI_RETRY_MAX=<n>      max retry attempts before giving up (default 5)
//   GEMINI_RETRY_MAX_WAIT=<seconds>  cap on a single backoff wait (default 30)
const (
	defaultMaxRetryAttempts = 5
	defaultMaxRetryWaitSec  = 30
)

var (
	baseBackoff      = 1 * time.Second
	maxRetryAttempts = defaultMaxRetryAttempts
	maxRetryWait     = time.Duration(defaultMaxRetryWaitSec) * time.Second
)

func init() {
	if v := os.Getenv("GEMINI_RETRY_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 100 {
			maxRetryAttempts = n
		}
	}
	if v := os.Getenv("GEMINI_RETRY_MAX_WAIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 600 {
			maxRetryWait = time.Duration(n) * time.Second
		}
	}
}

// retryAfterRE pulls the "reset after Ns" hint Google's RESOURCE_EXHAUSTED
// body sometimes embeds in `error.message`. Cheaper and more reliable than
// querying retry-info details.
var retryAfterRE = regexp.MustCompile(`reset after (\d+)s`)

// requestBuilder is a closure that builds a fresh *http.Request each attempt.
// We can't reuse a single request because the Body io.Reader is consumed.
type requestBuilder func() (*http.Request, error)

// doWithRetry is the single retry path used by every cloudcode-pa call.
// On retryable failure it sleeps and rebuilds the request; on success it
// returns the live response (caller is responsible for closing res.Body).
func (c *Client) doWithRetry(ctx context.Context, build requestBuilder) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetryAttempts; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			// Treat any transport-level error as transient.
			lastErr = err
			wait := backoffFor(attempt, 0)
			notifyRetry(attempt+1, wait, fmt.Sprintf("network: %v", err))
			if !sleepCtx(ctx, wait) {
				return nil, ctx.Err()
			}
			continue
		}

		// Success.
		if res.StatusCode < 400 {
			return res, nil
		}

		// Read body to inspect (and to consider retry-after).
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		statusErr := fmt.Errorf("HTTP %d %s: %s", res.StatusCode, res.Status, truncate(string(body), 500))

		retryable, hint := classify(res.StatusCode, res.Header, body)
		if !retryable {
			return nil, statusErr
		}
		lastErr = statusErr
		wait := backoffFor(attempt, hint)
		notifyRetry(attempt+1, wait, fmt.Sprintf("HTTP %d", res.StatusCode))
		if !sleepCtx(ctx, wait) {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", maxRetryAttempts, lastErr)
}

// classify returns (retryable, hintWait). hintWait is zero if no hint found.
func classify(status int, header http.Header, body []byte) (bool, time.Duration) {
	switch {
	case status == 429:
		return true, retryAfterFromAll(header, body)
	case status >= 500 && status < 600:
		return true, retryAfterFromAll(header, body)
	default:
		return false, 0
	}
}

func retryAfterFromAll(header http.Header, body []byte) time.Duration {
	if v := header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
		// HTTP-date format (rare) — not parsing for now.
	}
	if d := retryAfterFromBody(body); d > 0 {
		return d
	}
	return 0
}

func retryAfterFromBody(body []byte) time.Duration {
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Error.Message == "" {
		return 0
	}
	m := retryAfterRE.FindStringSubmatch(resp.Error.Message)
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// backoffFor returns the wait duration for a retry attempt, optionally biased
// by a server hint. Hint is honored as a floor; we then add jitter.
func backoffFor(attempt int, hint time.Duration) time.Duration {
	expo := baseBackoff << attempt // 1s, 2s, 4s, 8s, 16s
	if expo > maxRetryWait {
		expo = maxRetryWait
	}
	wait := expo
	if hint > 0 && hint > wait {
		wait = hint
	}
	if wait > maxRetryWait {
		wait = maxRetryWait
	}
	return jitter(wait)
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	// ±25%
	swing := int64(d) / 4
	if swing == 0 {
		return d
	}
	delta := rand.Int63n(2*swing) - swing
	return d + time.Duration(delta)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// notifyRetry emits a single line to stderr. Quiet by default; the user opts in.
// Set MINI_QUIET=1 to silence.
func notifyRetry(attempt int, wait time.Duration, reason string) {
	if os.Getenv("MINI_QUIET") == "1" {
		return
	}
	fmt.Fprintf(os.Stderr, "[gemini] retry %d in %s (%s)\n",
		attempt, wait.Round(100*time.Millisecond), reason)
}
