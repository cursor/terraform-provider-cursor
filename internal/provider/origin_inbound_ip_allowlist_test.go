package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

const sampleInboundIPEntryID = "nsip_01k2ja2000e0080000000000c4"

func TestOriginInboundIPAllowlistGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/namespaces/acme/inbound-ip-allowlist" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer session-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"enabled":true,"entries":[{"id":"nsip_01k2ja2000e0080000000000c4","cidr":"203.0.113.0/24","description":"Office","enabled":true,"createdAt":"2026-08-02T14:45:00Z"},{"id":"nsip_01k2ja2000e0080000000000c5","cidr":"198.51.100.7","createdAt":"2026-08-03T09:10:00Z"}]}`)
	}))
	defer server.Close()

	list, err := testOriginClient(server).getOriginInboundIPAllowlist(context.Background(), "acme")
	if err != nil {
		t.Fatalf("getOriginInboundIPAllowlist() error: %v", err)
	}
	if !list.Enabled || len(list.Entries) != 2 {
		t.Fatalf("list = %#v", list)
	}
	if got := list.Entries[0]; got.ID != sampleInboundIPEntryID || got.CIDR != "203.0.113.0/24" || got.Description != "Office" || !got.Enabled || got.CreatedAt != "2026-08-02T14:45:00Z" {
		t.Fatalf("first entry = %#v", got)
	}
	if got := list.Entries[1]; got.Enabled || got.Description != "" || got.CIDR != "198.51.100.7" {
		t.Fatalf("second entry = %#v, want omitted enabled and description read as false and empty", got)
	}
}

func TestOriginInboundIPAllowlistGetEmptyAndInvalid(t *testing.T) {
	body := `{}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	client := testOriginClient(server)
	list, err := client.getOriginInboundIPAllowlist(context.Background(), "acme")
	if err != nil {
		t.Fatalf("empty list error: %v", err)
	}
	if list.Enabled || list.Entries == nil || len(list.Entries) != 0 {
		t.Fatalf("empty list = %#v", list)
	}

	for raw, want := range map[string]string{
		`{"entries":[{"cidr":"203.0.113.0/24"}]}`: "without an id",
		`{"entries":[{"id":"nsip_01"}]}`:          "without a cidr",
		`not json`:                                "decoding Origin inbound IP allowlist",
	} {
		body = raw
		if _, err := client.getOriginInboundIPAllowlist(context.Background(), "acme"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("body %s: error = %v, want %q", raw, err, want)
		}
	}
}

func TestOriginInboundIPAllowlistSetEnabledSendsFalse(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()

	mock.add("acme", "203.0.113.0/24", "Office", true)
	client := mock.client()
	for _, want := range []bool{true, false} {
		list, err := client.setOriginInboundIPAllowlistEnabled(context.Background(), "acme", want)
		if err != nil {
			t.Fatalf("setOriginInboundIPAllowlistEnabled(%v) error: %v", want, err)
		}
		if list.Enabled != want || len(list.Entries) != 1 {
			t.Fatalf("list = %#v, want enabled %v", list, want)
		}
		if body := mock.lastBody("PATCH /namespaces/acme/inbound-ip-allowlist"); body != fmt.Sprintf(`{"enabled":%v}`, want) {
			t.Fatalf("body = %s", body)
		}
	}
}

func TestOriginInboundIPAllowlistEntryAdd(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()

	ctx := context.Background()
	client := mock.client()
	entry, err := client.addOriginInboundIPAllowlistEntry(ctx, "acme", originInboundIPAllowlistEntryAdd{CIDR: "203.0.113.0/24", Description: "Office", Enabled: true})
	if err != nil {
		t.Fatalf("addOriginInboundIPAllowlistEntry() error: %v", err)
	}
	if body := mock.lastBody("POST /namespaces/acme/inbound-ip-allowlist/entries"); body != `{"cidr":"203.0.113.0/24","description":"Office","enabled":true}` {
		t.Fatalf("add body = %s", body)
	}
	if !strings.HasPrefix(entry.ID, "nsip_") || entry.CIDR != "203.0.113.0/24" || entry.Description != "Office" || !entry.Enabled || entry.CreatedAt == "" {
		t.Fatalf("entry = %#v", entry)
	}

	disabled, err := client.addOriginInboundIPAllowlistEntry(ctx, "acme", originInboundIPAllowlistEntryAdd{CIDR: "2001:db8::/32"})
	if err != nil {
		t.Fatalf("add disabled entry error: %v", err)
	}
	if body := mock.lastBody("POST /namespaces/acme/inbound-ip-allowlist/entries"); body != `{"cidr":"2001:db8::/32","enabled":false}` {
		t.Fatalf("disabled add body = %s", body)
	}
	if disabled.Enabled || disabled.Description != "" {
		t.Fatalf("disabled entry = %#v", disabled)
	}

	for cidr, want := range map[string]string{"203.0.113.0/24": "HTTP 409", "0.0.0.0/0": "HTTP 400", "not-an-ip": "HTTP 400"} {
		if _, err := client.addOriginInboundIPAllowlistEntry(ctx, "acme", originInboundIPAllowlistEntryAdd{CIDR: cidr, Enabled: true}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("add %s: error = %v, want %s", cidr, err, want)
		}
	}
}

func TestOriginInboundIPAllowlistEntryRejectsMismatchedResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, originInboundIPAllowlistEntry{ID: "nsip_01other", CIDR: "198.51.100.7", Enabled: true})
	}))
	defer server.Close()

	ctx := context.Background()
	client := testOriginClient(server)
	if _, err := client.addOriginInboundIPAllowlistEntry(ctx, "acme", originInboundIPAllowlistEntryAdd{CIDR: "203.0.113.0/24"}); err == nil || !strings.Contains(err.Error(), `with cidr "198.51.100.7", want "203.0.113.0/24"`) {
		t.Fatalf("add error = %v, want foreign cidr rejection", err)
	}
	if _, err := client.getOriginInboundIPAllowlistEntry(ctx, "acme", sampleInboundIPEntryID); err == nil || !strings.Contains(err.Error(), "returned inbound IP allowlist entry nsip_01other for "+sampleInboundIPEntryID) {
		t.Fatalf("get error = %v, want foreign id rejection", err)
	}
	cidr := "198.51.100.8"
	if _, err := client.updateOriginInboundIPAllowlistEntry(ctx, "acme", "nsip_01other", originInboundIPAllowlistEntryPatch{CIDR: &cidr}); err == nil || !strings.Contains(err.Error(), `want "198.51.100.8"`) {
		t.Fatalf("update error = %v, want foreign cidr rejection", err)
	}
}

func TestOriginInboundIPAllowlistEntryUpdateSendsOnlySetFields(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	stored := mock.add("acme", "203.0.113.0/24", "Office", true)

	ctx := context.Background()
	client := mock.client()
	route := "PATCH /namespaces/acme/inbound-ip-allowlist/entries/" + stored.ID
	cidr := "203.0.113.0/25"
	entry, err := client.updateOriginInboundIPAllowlistEntry(ctx, "acme", stored.ID, originInboundIPAllowlistEntryPatch{CIDR: &cidr})
	if err != nil {
		t.Fatalf("update cidr error: %v", err)
	}
	if body := mock.lastBody(route); body != `{"cidr":"203.0.113.0/25"}` {
		t.Fatalf("cidr body = %s", body)
	}
	if entry.ID != stored.ID || entry.CIDR != cidr || entry.Description != "Office" || !entry.Enabled {
		t.Fatalf("entry = %#v, want the same id with the new cidr", entry)
	}

	empty, disabled := "", false
	entry, err = client.updateOriginInboundIPAllowlistEntry(ctx, "acme", stored.ID, originInboundIPAllowlistEntryPatch{Description: &empty, Enabled: &disabled})
	if err != nil {
		t.Fatalf("update description/enabled error: %v", err)
	}
	if body := mock.lastBody(route); body != `{"description":"","enabled":false}` {
		t.Fatalf("description/enabled body = %s", body)
	}
	if entry.Description != "" || entry.Enabled || entry.CIDR != cidr {
		t.Fatalf("entry = %#v", entry)
	}

	other := mock.add("acme", "198.51.100.7", "", true)
	if _, err := client.updateOriginInboundIPAllowlistEntry(ctx, "acme", other.ID, originInboundIPAllowlistEntryPatch{CIDR: &cidr}); err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("duplicate cidr error = %v, want HTTP 409", err)
	}
	if _, err := client.updateOriginInboundIPAllowlistEntry(ctx, "acme", "nsip_01missing", originInboundIPAllowlistEntryPatch{Enabled: &disabled}); !isOriginNotFound(err) {
		t.Fatalf("missing entry error = %v, want not found", err)
	}
	if _, err := client.updateOriginInboundIPAllowlistEntry(ctx, "acme", "", originInboundIPAllowlistEntryPatch{Enabled: &disabled}); err == nil {
		t.Fatal("expected empty id to be rejected before any request")
	}
}

func TestOriginInboundIPAllowlistEntryDelete(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	stored := mock.add("acme", "203.0.113.0/24", "Office", true)

	ctx := context.Background()
	client := mock.client()
	if err := client.deleteOriginInboundIPAllowlistEntry(ctx, "acme", stored.ID); err != nil {
		t.Fatalf("delete error: %v", err)
	}
	if len(mock.entries("acme")) != 0 {
		t.Fatalf("entries after delete = %#v", mock.entries("acme"))
	}
	if err := client.deleteOriginInboundIPAllowlistEntry(ctx, "acme", stored.ID); !isOriginNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
	if err := client.deleteOriginInboundIPAllowlistEntry(ctx, "acme", ""); err == nil {
		t.Fatal("expected empty id to be rejected before any request")
	}
}

func TestOriginInboundIPAllowlistEntryLookup(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	stored := mock.add("acme", "203.0.113.0/24", "Office", true)

	ctx := context.Background()
	client := mock.client()
	entry, err := client.lookupOriginInboundIPAllowlistEntry(ctx, "acme", stored.ID)
	if err != nil || entry == nil || entry.ID != stored.ID || entry.CIDR != "203.0.113.0/24" {
		t.Fatalf("lookup = %#v, %v", entry, err)
	}
	if mock.count("GET /namespaces/acme/inbound-ip-allowlist") != 0 {
		t.Fatal("a found entry must not list the namespace")
	}

	entry, err = client.lookupOriginInboundIPAllowlistEntry(ctx, "acme", "nsip_01removed")
	if err != nil || entry != nil {
		t.Fatalf("removed entry lookup = %#v, %v, want nil, nil", entry, err)
	}
	if mock.count("GET /namespaces/acme/inbound-ip-allowlist") != 1 {
		t.Fatal("a 404 on the entry must be confirmed through the namespace list")
	}

	mock.hide("acme")
	entry, err = client.lookupOriginInboundIPAllowlistEntry(ctx, "acme", stored.ID)
	if !isOriginNotFound(err) || entry != nil {
		t.Fatalf("hidden namespace lookup = %#v, %v, want the namespace 404", entry, err)
	}
}

type inboundIPAllowlistMock struct {
	*httptest.Server
	t      *testing.T
	mu     sync.Mutex
	hits   map[string][]string
	lists  map[string]*mockInboundIPAllowlist
	hidden map[string]bool
	caller netip.Addr
	nextID int
}

type mockInboundIPAllowlist struct {
	enabled bool
	entries []originInboundIPAllowlistEntry
}

type mockInboundIPAllowlistJSON struct {
	Enabled bool                              `json:"enabled,omitempty"`
	Entries []mockInboundIPAllowlistEntryJSON `json:"entries,omitempty"`
}

type mockInboundIPAllowlistEntryJSON struct {
	ID          string `json:"id"`
	CIDR        string `json:"cidr"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled,omitempty"`
	CreatedAt   string `json:"createdAt"`
}

func newInboundIPAllowlistMock(t *testing.T) *inboundIPAllowlistMock {
	m := &inboundIPAllowlistMock{t: t, hits: map[string][]string{}, lists: map[string]*mockInboundIPAllowlist{}, hidden: map[string]bool{}}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	return m
}

func (m *inboundIPAllowlistMock) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	defer m.mu.Unlock()
	route := r.Method + " " + r.URL.Path
	m.hits[route] = append(m.hits[route], strings.TrimSpace(string(raw)))

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/namespaces/"), "/")
	if len(parts) < 2 || len(parts) > 4 || parts[1] != "inbound-ip-allowlist" || (len(parts) > 2 && parts[2] != "entries") {
		http.NotFound(w, r)
		return
	}
	if m.hidden[parts[0]] {
		writeJSON(m.t, w, http.StatusNotFound, originStatusError{Code: 5, Message: "namespace not found"})
		return
	}
	list := m.listLocked(parts[0])
	switch {
	case len(parts) == 2 && r.Method == http.MethodGet:
		writeJSON(m.t, w, http.StatusOK, list.json())
	case len(parts) == 2 && r.Method == http.MethodPatch:
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.Enabled == nil {
			writeJSON(m.t, w, http.StatusBadRequest, originStatusError{Code: 3, Message: "enabled is required"})
			return
		}
		if !m.guardCaller(w, *list, mockInboundIPAllowlist{enabled: *body.Enabled, entries: list.entries}) {
			return
		}
		list.enabled = *body.Enabled
		writeJSON(m.t, w, http.StatusOK, list.json())
	case len(parts) == 3 && r.Method == http.MethodPost:
		var body struct {
			CIDR        string `json:"cidr"`
			Description string `json:"description"`
			Enabled     *bool  `json:"enabled"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			writeJSON(m.t, w, http.StatusBadRequest, originStatusError{Code: 3, Message: "invalid body"})
			return
		}
		cidr := strings.TrimSpace(body.CIDR)
		if msg := mockInboundCIDRError(cidr); msg != "" {
			writeJSON(m.t, w, http.StatusBadRequest, originStatusError{Code: 3, Message: msg})
			return
		}
		if list.indexOf(func(e originInboundIPAllowlistEntry) bool { return e.CIDR == cidr }) >= 0 {
			writeJSON(m.t, w, http.StatusConflict, originStatusError{Code: 6, Message: "the namespace already lists " + cidr})
			return
		}
		if len(list.entries) >= maxOriginInboundIPAllowlistEntries {
			writeJSON(m.t, w, http.StatusBadRequest, originStatusError{Code: 3, Message: "the inbound IP allowlist already holds 100 entries"})
			return
		}
		if !m.guardCaller(w, *list, *list) {
			return
		}
		entry := m.newEntryLocked(cidr, body.Description, body.Enabled == nil || *body.Enabled)
		list.entries = append(list.entries, entry)
		writeJSON(m.t, w, http.StatusOK, mockInboundEntryJSON(entry))
	case len(parts) == 4:
		index := list.indexOf(func(e originInboundIPAllowlistEntry) bool { return e.ID == parts[3] })
		if index < 0 {
			writeJSON(m.t, w, http.StatusNotFound, originStatusError{Code: 5, Message: "entry not found"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(m.t, w, http.StatusOK, mockInboundEntryJSON(list.entries[index]))
		case http.MethodPatch:
			var body struct {
				CIDR        *string `json:"cidr"`
				Description *string `json:"description"`
				Enabled     *bool   `json:"enabled"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				writeJSON(m.t, w, http.StatusBadRequest, originStatusError{Code: 3, Message: "invalid body"})
				return
			}
			entry := list.entries[index]
			if body.CIDR != nil {
				cidr := strings.TrimSpace(*body.CIDR)
				if msg := mockInboundCIDRError(cidr); msg != "" {
					writeJSON(m.t, w, http.StatusBadRequest, originStatusError{Code: 3, Message: msg})
					return
				}
				if other := list.indexOf(func(e originInboundIPAllowlistEntry) bool { return e.CIDR == cidr }); other >= 0 && other != index {
					writeJSON(m.t, w, http.StatusConflict, originStatusError{Code: 6, Message: "another entry already lists " + cidr})
					return
				}
				entry.CIDR = cidr
			}
			if body.Description != nil {
				entry.Description = *body.Description
			}
			if body.Enabled != nil {
				entry.Enabled = *body.Enabled
			}
			if !m.guardCaller(w, *list, list.with(index, &entry)) {
				return
			}
			list.entries[index] = entry
			writeJSON(m.t, w, http.StatusOK, mockInboundEntryJSON(entry))
		case http.MethodDelete:
			next := list.with(index, nil)
			if !m.guardCaller(w, *list, next) {
				return
			}
			list.entries = next.entries
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func (m *inboundIPAllowlistMock) guardCaller(w http.ResponseWriter, current, next mockInboundIPAllowlist) bool {
	if m.excludesCaller(current) {
		writeJSON(m.t, w, http.StatusForbidden, originStatusError{Code: 7, Message: "the inbound IP allowlist does not admit your address"})
		return false
	}
	if m.excludesCaller(next) {
		writeJSON(m.t, w, http.StatusBadRequest, originStatusError{Code: 3, Message: "the change would exclude your own address from the enforced inbound IP allowlist"})
		return false
	}
	return true
}

func (m *inboundIPAllowlistMock) excludesCaller(list mockInboundIPAllowlist) bool {
	if !m.caller.IsValid() || !list.enabled {
		return false
	}
	active := false
	for _, entry := range list.entries {
		if !entry.Enabled {
			continue
		}
		active = true
		if prefix, err := netip.ParsePrefix(entry.CIDR); err == nil && prefix.Contains(m.caller) {
			return false
		}
		if addr, err := netip.ParseAddr(entry.CIDR); err == nil && addr == m.caller {
			return false
		}
	}
	return active
}

func mockInboundCIDRError(cidr string) string {
	if prefix, err := netip.ParsePrefix(cidr); err == nil {
		if prefix.Bits() == 0 {
			return "a range covering an entire address space is not allowed"
		}
		return ""
	}
	if _, err := netip.ParseAddr(cidr); err == nil {
		return ""
	}
	return fmt.Sprintf("cidr %q is not an IP address or CIDR range", cidr)
}

func (l *mockInboundIPAllowlist) indexOf(match func(originInboundIPAllowlistEntry) bool) int {
	for i, entry := range l.entries {
		if match(entry) {
			return i
		}
	}
	return -1
}

func (l *mockInboundIPAllowlist) with(index int, replacement *originInboundIPAllowlistEntry) mockInboundIPAllowlist {
	entries := make([]originInboundIPAllowlistEntry, 0, len(l.entries))
	for i, entry := range l.entries {
		switch {
		case i != index:
			entries = append(entries, entry)
		case replacement != nil:
			entries = append(entries, *replacement)
		}
	}
	return mockInboundIPAllowlist{enabled: l.enabled, entries: entries}
}

func (l *mockInboundIPAllowlist) json() mockInboundIPAllowlistJSON {
	out := mockInboundIPAllowlistJSON{Enabled: l.enabled}
	for _, entry := range l.entries {
		out.Entries = append(out.Entries, mockInboundEntryJSON(entry))
	}
	return out
}

func mockInboundEntryJSON(entry originInboundIPAllowlistEntry) mockInboundIPAllowlistEntryJSON {
	return mockInboundIPAllowlistEntryJSON(entry)
}

func (m *inboundIPAllowlistMock) listLocked(namespace string) *mockInboundIPAllowlist {
	list, ok := m.lists[namespace]
	if !ok {
		list = &mockInboundIPAllowlist{}
		m.lists[namespace] = list
	}
	return list
}

func (m *inboundIPAllowlistMock) newEntryLocked(cidr, description string, enabled bool) originInboundIPAllowlistEntry {
	m.nextID++
	return originInboundIPAllowlistEntry{
		ID:          fmt.Sprintf("nsip_%026d", m.nextID),
		CIDR:        cidr,
		Description: description,
		Enabled:     enabled,
		CreatedAt:   fmt.Sprintf("2026-08-02T14:45:%02dZ", m.nextID),
	}
}

func (m *inboundIPAllowlistMock) add(namespace, cidr, description string, enabled bool) originInboundIPAllowlistEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.listLocked(namespace)
	entry := m.newEntryLocked(cidr, description, enabled)
	list.entries = append(list.entries, entry)
	return entry
}

func (m *inboundIPAllowlistMock) setEnabled(namespace string, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listLocked(namespace).enabled = enabled
}

func (m *inboundIPAllowlistMock) setCaller(addr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.caller = netip.MustParseAddr(addr)
}

func (m *inboundIPAllowlistMock) hide(namespace string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hidden[namespace] = true
}

func (m *inboundIPAllowlistMock) enabled(namespace string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked(namespace).enabled
}

func (m *inboundIPAllowlistMock) entries(namespace string) []originInboundIPAllowlistEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]originInboundIPAllowlistEntry(nil), m.listLocked(namespace).entries...)
}

func (m *inboundIPAllowlistMock) client() *apiClient {
	return &apiClient{
		httpClient: m.Client(),
		authHeader: "Bearer session-token",
		userAgent:  "terraform-provider-cursor/test",
		originBase: m.URL,
	}
}

func (m *inboundIPAllowlistMock) lastBody(route string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	bodies := m.hits[route]
	if len(bodies) == 0 {
		m.t.Fatalf("no request recorded for %s", route)
	}
	return bodies[len(bodies)-1]
}

func (m *inboundIPAllowlistMock) count(route string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.hits[route])
}
