package provider

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const (
	maxRateLimitRetries   = 8
	maxServerErrorRetries = 3
	retryBaseDelay        = time.Second
	maxRetryBackoff       = 30 * time.Second
	maxRetryWait          = 2 * time.Minute
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type retryClient struct {
	next httpDoer
	wait func(context.Context, time.Duration) error
}

func newRetryClient(next httpDoer) *retryClient {
	return &retryClient{next: next, wait: sleepContext}
}

func (c *retryClient) Do(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	for attempt := 1; ; attempt++ {
		resp, err := c.next.Do(req)
		if err != nil {
			return resp, err
		}
		maxRetries := retryLimit(req.Method, resp.StatusCode)
		if maxRetries == 0 {
			return resp, nil
		}
		fields := map[string]any{
			"method":      req.Method,
			"host":        req.URL.Host,
			"path":        req.URL.Path,
			"status_code": resp.StatusCode,
			"attempt":     attempt,
			"max_retries": maxRetries,
		}
		if id := resp.Header.Get("X-Request-Id"); id != "" {
			fields["request_id"] = id
		}
		wait, reason := retryWait(ctx, req, resp, attempt, maxRetries)
		if reason != "" {
			fields["reason"] = reason
			tflog.Debug(ctx, "Not retrying Cursor API request", fields)
			return resp, nil
		}
		fields["wait"] = wait.Round(time.Millisecond).String()
		tflog.Warn(ctx, "Retrying Cursor API request", fields)
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if err := c.wait(ctx, wait); err != nil {
			return nil, fmt.Errorf("waiting to retry HTTP %d response: %w", resp.StatusCode, err)
		}
		if req, err = rewindRequest(req); err != nil {
			return nil, err
		}
	}
}

func retryLimit(method string, status int) int {
	switch status {
	case http.StatusTooManyRequests:
		return maxRateLimitRetries
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
			return maxServerErrorRetries
		}
	}
	return 0
}

func retryWait(ctx context.Context, req *http.Request, resp *http.Response, attempt, maxRetries int) (time.Duration, string) {
	if attempt > maxRetries {
		return 0, "retry limit reached"
	}
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return 0, "request body cannot be replayed"
	}
	wait := retryDelay(resp.Header, attempt, time.Now())
	if wait > maxRetryWait {
		return 0, fmt.Sprintf("server asked to wait %s, more than %s", wait.Round(time.Second), maxRetryWait)
	}
	wait = withJitter(wait)
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < wait {
		return 0, "context deadline is before the retry time"
	}
	return wait, ""
}

func retryDelay(header http.Header, attempt int, now time.Time) time.Duration {
	if wait, ok := parseRetryAfter(header.Get("Retry-After"), now); ok {
		return wait
	}
	if strings.TrimSpace(header.Get("X-RateLimit-Remaining")) == "0" {
		if reset, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			if wait := time.Unix(reset, 0).Sub(now); wait > 0 {
				return wait
			}
		}
	}
	return min(retryBaseDelay<<(attempt-1), maxRetryBackoff)
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

func withJitter(wait time.Duration) time.Duration {
	if wait <= 0 {
		return 0
	}
	return wait + rand.N(wait/5+1)
}

func rewindRequest(req *http.Request) (*http.Request, error) {
	next := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("replaying request body: %w", err)
		}
		next.Body = body
	}
	return next, nil
}

func sleepContext(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
