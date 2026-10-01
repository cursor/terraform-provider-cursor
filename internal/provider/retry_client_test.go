package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	v1 "github.com/cursor/terraform-provider-cursor/internal/proto/v1"
	v1connect "github.com/cursor/terraform-provider-cursor/internal/proto/v1/v1connect"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"
)

type stubResponse struct {
	status int
	header map[string]string
	body   string
}

func serveSequence(t *testing.T, responses ...stubResponse) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		next := responses[min(len(bodies), len(responses)-1)]
		bodies = append(bodies, string(body))
		mu.Unlock()
		for key, value := range next.header {
			w.Header().Set(key, value)
		}
		w.WriteHeader(next.status)
		_, _ = io.WriteString(w, next.body)
	}))
	// On a reused connection net/http rewinds request bodies itself, which would mask a missing rewind.
	server.Config.SetKeepAlivesEnabled(false)
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(bodies)
	}
}

type waitRecorder []time.Duration

func (r *waitRecorder) wait(_ context.Context, d time.Duration) error {
	*r = append(*r, d)
	return nil
}

func TestRetryClientWaitsForServerDelay(t *testing.T) {
	now := time.Now()
	unix := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) }
	tests := []struct {
		name     string
		header   map[string]string
		min, max time.Duration
	}{
		{"retry-after seconds", map[string]string{"Retry-After": "7"}, 7 * time.Second, 8400 * time.Millisecond},
		{"retry-after http date", map[string]string{"Retry-After": now.Add(5 * time.Second).UTC().Format(http.TimeFormat)}, 3 * time.Second, 6 * time.Second},
		{"rate limit reset", map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": unix(10 * time.Second)}, 8 * time.Second, 12 * time.Second},
		{"retry-after wins over reset", map[string]string{"Retry-After": "3", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": unix(30 * time.Second)}, 3 * time.Second, 3600 * time.Millisecond},
		{"reset ignored while quota remains", map[string]string{"X-RateLimit-Remaining": "4", "X-RateLimit-Reset": unix(30 * time.Second)}, retryBaseDelay, retryBaseDelay * 6 / 5},
		{"unparseable retry-after", map[string]string{"Retry-After": "soon"}, retryBaseDelay, retryBaseDelay * 6 / 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, bodies := serveSequence(t,
				stubResponse{status: http.StatusTooManyRequests, header: tt.header},
				stubResponse{status: http.StatusOK},
			)
			var waits waitRecorder
			client := &retryClient{next: server.Client(), wait: waits.wait}
			req, err := http.NewRequest(http.MethodGet, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Do() error: %v", err)
			}
			defer resp.Body.Close()
			if n := len(bodies()); resp.StatusCode != http.StatusOK || n != 2 {
				t.Fatalf("status = %d after %d requests, want 200 after 2", resp.StatusCode, n)
			}
			if len(waits) != 1 || waits[0] < tt.min || waits[0] > tt.max {
				t.Fatalf("waits = %v, want one wait in [%s, %s]", waits, tt.min, tt.max)
			}
		})
	}
}

func TestRetryClientRetryLimits(t *testing.T) {
	const payload = `{"cidr":"203.0.113.0/24"}`
	tests := []struct {
		method       string
		status       int
		wantRequests int
	}{
		{http.MethodPost, http.StatusTooManyRequests, maxRateLimitRetries + 1},
		{http.MethodPatch, http.StatusTooManyRequests, maxRateLimitRetries + 1},
		{http.MethodGet, http.StatusServiceUnavailable, maxServerErrorRetries + 1},
		{http.MethodPut, http.StatusBadGateway, maxServerErrorRetries + 1},
		{http.MethodDelete, http.StatusGatewayTimeout, maxServerErrorRetries + 1},
		{http.MethodPost, http.StatusServiceUnavailable, 1},
		{http.MethodPatch, http.StatusBadGateway, 1},
		{http.MethodGet, http.StatusInternalServerError, 1},
		{http.MethodGet, http.StatusNotFound, 1},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s %d", tt.method, tt.status), func(t *testing.T) {
			server, bodies := serveSequence(t, stubResponse{status: tt.status, body: "busy"})
			var waits waitRecorder
			client := &retryClient{next: server.Client(), wait: waits.wait}
			req, err := http.NewRequest(tt.method, server.URL, strings.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Do() error: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tt.status || string(body) != "busy" {
				t.Fatalf("response = %d %q, want the final %d response", resp.StatusCode, body, tt.status)
			}
			got := bodies()
			if len(got) != tt.wantRequests {
				t.Fatalf("server saw %d requests, want %d", len(got), tt.wantRequests)
			}
			for i, b := range got {
				if b != payload {
					t.Errorf("request %d body = %q, want %q", i+1, b, payload)
				}
			}
			if len(waits) != tt.wantRequests-1 {
				t.Fatalf("waits = %v, want %d", waits, tt.wantRequests-1)
			}
			for i, wait := range waits {
				base := min(retryBaseDelay<<i, maxRetryBackoff)
				if wait < base || wait > base*6/5 {
					t.Errorf("wait %d = %s, want %s plus up to 20%% jitter", i+1, wait, base)
				}
			}
		})
	}
}

func TestRetryClientReturnsResponseWithoutRetrying(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		timeout    time.Duration
		body       io.Reader
	}{
		{name: "wait exceeds maximum", retryAfter: "3600"},
		{name: "deadline before retry time", retryAfter: "30", timeout: 5 * time.Second},
		{name: "body cannot be replayed", retryAfter: "1", body: io.MultiReader(strings.NewReader("payload"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, bodies := serveSequence(t, stubResponse{
				status: http.StatusTooManyRequests,
				header: map[string]string{"Retry-After": tt.retryAfter},
				body:   "slow down",
			})
			ctx := context.Background()
			if tt.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.timeout)
				defer cancel()
			}
			var waits waitRecorder
			client := &retryClient{next: server.Client(), wait: waits.wait}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, tt.body)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Do() error: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusTooManyRequests || string(body) != "slow down" {
				t.Fatalf("response = %d %q, want the 429 response", resp.StatusCode, body)
			}
			if n := len(bodies()); n != 1 || len(waits) != 0 {
				t.Fatalf("server saw %d requests and client waited %v, want 1 request and no wait", n, waits)
			}
		})
	}
}

func TestRetryClientStopsWaitingWhenContextIsCanceled(t *testing.T) {
	server, bodies := serveSequence(t, stubResponse{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "60"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requested time.Duration
	client := newRetryClient(server.Client())
	client.wait = func(ctx context.Context, d time.Duration) error {
		requested = d
		time.AfterFunc(20*time.Millisecond, cancel)
		return sleepContext(ctx, d)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	resp, err := client.Do(req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() = %v, %v; want context.Canceled", resp, err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Do() returned after %s, want it to stop waiting once the context is canceled", elapsed)
	}
	if n := len(bodies()); requested < 60*time.Second || n != 1 {
		t.Errorf("waited %s after %d requests, want a 60s wait after 1 request", requested, n)
	}
}

func TestRetryClientLogsRetries(t *testing.T) {
	const path = "/namespaces/acme/inbound-ip-allowlist/entries"
	server, _ := serveSequence(t,
		stubResponse{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "2", "X-Request-Id": "req_123"}},
		stubResponse{status: http.StatusCreated},
	)
	var output bytes.Buffer
	ctx := tflogtest.RootLogger(context.Background(), &output)
	var waits waitRecorder
	client := &retryClient{next: server.Client(), wait: waits.wait}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do() error: %v", err)
	}
	resp.Body.Close()

	entries, err := tflogtest.MultilineJSONDecode(&output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("log entries = %v, want 1", entries)
	}
	want := map[string]any{
		"@level":      "warn",
		"@message":    "Retrying Cursor API request",
		"method":      http.MethodPost,
		"path":        path,
		"status_code": float64(http.StatusTooManyRequests),
		"attempt":     float64(1),
		"max_retries": float64(maxRateLimitRetries),
		"request_id":  "req_123",
	}
	for key, value := range want {
		if entries[0][key] != value {
			t.Errorf("log %s = %v, want %v", key, entries[0][key], value)
		}
	}
	if _, ok := entries[0]["wait"]; !ok {
		t.Errorf("log entry %v has no wait", entries[0])
	}
}

func TestOriginInboundIPAllowlistEntryAddRetriesRateLimit(t *testing.T) {
	server, bodies := serveSequence(t,
		stubResponse{
			status: http.StatusTooManyRequests,
			header: map[string]string{
				"Content-Type":          "application/json",
				"Retry-After":           "60",
				"X-RateLimit-Limit":     "600",
				"X-RateLimit-Remaining": "0",
				"X-RateLimit-Reset":     strconv.FormatInt(time.Now().Add(45*time.Second).Unix(), 10),
				"X-RateLimit-Resource":  "core",
			},
			body: `{"message":"Rate limit exceeded: 600 points per minute for this user. Retry after 60s."}`,
		},
		stubResponse{
			status: http.StatusCreated,
			header: map[string]string{"Content-Type": "application/json"},
			body:   `{"id":"` + sampleInboundIPEntryID + `","cidr":"203.0.113.0/24","enabled":true}`,
		},
	)
	var waits waitRecorder
	client := testOriginClient(server)
	client.httpClient = &retryClient{next: client.httpClient, wait: waits.wait}

	entry, err := client.addOriginInboundIPAllowlistEntry(context.Background(), "acme", originInboundIPAllowlistEntryAdd{CIDR: "203.0.113.0/24", Enabled: true})
	if err != nil {
		t.Fatalf("addOriginInboundIPAllowlistEntry() error: %v", err)
	}
	if entry.ID != sampleInboundIPEntryID {
		t.Errorf("entry ID = %q, want %q", entry.ID, sampleInboundIPEntryID)
	}
	if got := bodies(); len(got) != 2 || got[0] != got[1] || !strings.Contains(got[0], `"cidr":"203.0.113.0/24"`) {
		t.Fatalf("request bodies = %q, want the entry sent twice", got)
	}
	if len(waits) != 1 || waits[0] < 60*time.Second || waits[0] > 72*time.Second {
		t.Fatalf("waits = %v, want Retry-After 60s plus jitter", waits)
	}
}

type rateLimitedAutomations struct {
	v1connect.UnimplementedAutomationsServiceHandler
	mu  sync.Mutex
	ids []string
}

func (h *rateLimitedAutomations) GetAutomation(_ context.Context, req *connect.Request[v1.GetAutomationRequest]) (*connect.Response[v1.GetAutomationResponse], error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ids = append(h.ids, req.Msg.GetAutomationId())
	if len(h.ids) == 1 {
		err := connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded"))
		err.Meta().Set("Retry-After", "2")
		return nil, err
	}
	return connect.NewResponse(&v1.GetAutomationResponse{}), nil
}

func TestRetryClientReplaysConnectRequest(t *testing.T) {
	handler := &rateLimitedAutomations{}
	mux := http.NewServeMux()
	mux.Handle(v1connect.NewAutomationsServiceHandler(handler))
	server := httptest.NewServer(mux)
	server.Config.SetKeepAlivesEnabled(false)
	defer server.Close()

	var waits waitRecorder
	client := v1connect.NewAutomationsServiceClient(&retryClient{next: server.Client(), wait: waits.wait}, server.URL)
	if _, err := client.GetAutomation(context.Background(), connect.NewRequest(&v1.GetAutomationRequest{AutomationId: "auto_123"})); err != nil {
		t.Fatalf("GetAutomation() error: %v", err)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if !slices.Equal(handler.ids, []string{"auto_123", "auto_123"}) {
		t.Fatalf("server saw automation IDs %q, want the request replayed once", handler.ids)
	}
	if len(waits) != 1 || waits[0] < 2*time.Second || waits[0] > 2400*time.Millisecond {
		t.Fatalf("waits = %v, want Retry-After 2s plus jitter", waits)
	}
}
