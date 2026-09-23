package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	sampleSSHCAKey         = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPwoQNzBuiWhDF4EKwRyt8h48XRY7Bc4yWbQ9s3Tnj7Q"
	sampleSSHCAFingerprint = "SHA256:D5vlIclvaSZlwq4gmckavfLE7n7F542Eyhk/PvXkRq0"
	sampleSSHCAID          = "nsca_01k2ja2000e0080000000000s5"
	secondSSHCAKey         = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILMfPYGGrZMNHV6VCyFdyWFAaeGpjcbRrG42Pjzo/4iy"
	secondSSHCAFingerprint = "SHA256:UX+v1q5Z5cwZCSNcZ7nk2wKoub68DmGvswyRvSiSxlI"
)

func sampleSSHCA() originSSHCertificateAuthority {
	return originSSHCertificateAuthority{
		ID:          sampleSSHCAID,
		Name:        "Acme production CA",
		KeyType:     "ssh-ed25519",
		Fingerprint: sampleSSHCAFingerprint,
		PublicKey:   sampleSSHCAKey,
		CreatedAt:   "2026-08-02T14:45:00Z",
	}
}

func TestOriginSSHCertificateAuthorityList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/owners/acme/ssh-certificate-authorities" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer session-token" {
			t.Errorf("Authorization = %q", got)
		}
		writeJSON(t, w, http.StatusOK, originSSHCertificateAuthorityList{
			CertificateAuthorities: []originSSHCertificateAuthority{sampleSSHCA()},
			RequireCertificates:    true,
		})
	}))
	defer server.Close()

	list, err := testOriginClient(server).listOriginSSHCertificateAuthorities(context.Background(), "acme")
	if err != nil {
		t.Fatalf("listOriginSSHCertificateAuthorities() error: %v", err)
	}
	if !list.RequireCertificates || len(list.CertificateAuthorities) != 1 || list.CertificateAuthorities[0].ID != sampleSSHCAID {
		t.Fatalf("list = %#v", list)
	}
}

func TestOriginSSHCertificateAuthorityListEmptyAndInvalid(t *testing.T) {
	var body any = map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, body)
	}))
	defer server.Close()

	list, err := testOriginClient(server).listOriginSSHCertificateAuthorities(context.Background(), "acme")
	if err != nil {
		t.Fatalf("empty list error: %v", err)
	}
	if list.CertificateAuthorities == nil || len(list.CertificateAuthorities) != 0 || list.RequireCertificates {
		t.Fatalf("empty list = %#v", list)
	}

	body = originSSHCertificateAuthorityList{CertificateAuthorities: []originSSHCertificateAuthority{{ID: "nsca_01", Name: "no key"}}}
	if _, err := testOriginClient(server).listOriginSSHCertificateAuthorities(context.Background(), "acme"); err == nil || !strings.Contains(err.Error(), "without a public key") {
		t.Fatalf("error = %v, want missing public key", err)
	}
}

func TestOriginSSHCertificateAuthorityAdd(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()

	authority, err := mock.client().addOriginSSHCertificateAuthority(context.Background(), "acme", originSSHCertificateAuthorityWrite{
		PublicKey: sampleSSHCAKey + " acme-ssh-ca",
		Name:      "Acme production CA",
	})
	if err != nil {
		t.Fatalf("addOriginSSHCertificateAuthority() error: %v", err)
	}
	if body := mock.lastBody("POST /owners/acme/ssh-certificate-authorities"); body != `{"publicKey":"`+sampleSSHCAKey+` acme-ssh-ca","name":"Acme production CA"}` {
		t.Fatalf("add body = %s", body)
	}
	if authority.ID == "" || authority.PublicKey != sampleSSHCAKey || authority.Fingerprint != sampleSSHCAFingerprint || authority.KeyType != "ssh-ed25519" {
		t.Fatalf("authority = %#v", authority)
	}

	_, err = mock.client().addOriginSSHCertificateAuthority(context.Background(), "acme", originSSHCertificateAuthorityWrite{PublicKey: sampleSSHCAKey, Name: "again"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("duplicate error = %v, want HTTP 409", err)
	}
}

func TestOriginSSHCertificateAuthorityAddRejectsForeignKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authority := sampleSSHCA()
		authority.PublicKey = secondSSHCAKey
		writeJSON(t, w, http.StatusOK, authority)
	}))
	defer server.Close()

	_, err := testOriginClient(server).addOriginSSHCertificateAuthority(context.Background(), "acme", originSSHCertificateAuthorityWrite{PublicKey: sampleSSHCAKey, Name: "ca"})
	if err == nil || !strings.Contains(err.Error(), "different public key") {
		t.Fatalf("error = %v, want foreign key rejection", err)
	}
}

func TestOriginSSHCertificateAuthorityDelete(t *testing.T) {
	status := http.StatusNoContent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/owners/acme/ssh-certificate-authorities/"+sampleSSHCAID {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if status == http.StatusNoContent {
			w.WriteHeader(status)
			return
		}
		writeJSON(t, w, status, originStatusError{Code: 5, Message: "missing"})
	}))
	defer server.Close()

	client := testOriginClient(server)
	if err := client.deleteOriginSSHCertificateAuthority(context.Background(), "acme", sampleSSHCAID); err != nil {
		t.Fatalf("delete error: %v", err)
	}
	status = http.StatusNotFound
	if err := client.deleteOriginSSHCertificateAuthority(context.Background(), "acme", sampleSSHCAID); !isOriginNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
	if err := client.deleteOriginSSHCertificateAuthority(context.Background(), "acme", ""); err == nil {
		t.Fatal("expected empty id to be rejected before any request")
	}
}

func TestOriginSSHCertificateRequirementSetSendsFalse(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()

	mock.add("acme", sampleSSHCAKey, "ca")
	client := mock.client()
	for _, want := range []bool{true, false} {
		got, err := client.setOriginSSHCertificateRequirement(context.Background(), "acme", want)
		if err != nil {
			t.Fatalf("setOriginSSHCertificateRequirement(%v) error: %v", want, err)
		}
		if got != want {
			t.Fatalf("require = %v, want %v", got, want)
		}
		if body := mock.lastBody("POST /owners/acme/ssh-certificate-authorities:setRequirement"); body != fmt.Sprintf(`{"requireCertificates":%v}`, want) {
			t.Fatalf("body = %s", body)
		}
	}
}

func TestNormalizeSSHPublicKey(t *testing.T) {
	cases := map[string]string{
		sampleSSHCAKey:                           sampleSSHCAKey,
		"  " + sampleSSHCAKey + " acme-ssh-ca\n": sampleSSHCAKey,
		sampleSSHCAKey + "\tcomment":             sampleSSHCAKey,
		"not-a-key":                              "not-a-key",
		"  \n":                                   "",
	}
	for in, want := range cases {
		if got := normalizeSSHPublicKey(in); got != want {
			t.Errorf("normalizeSSHPublicKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// sshCAMock serves the owner SSH certificate authority endpoints with the Origin API's status codes.
type sshCAMock struct {
	*httptest.Server
	t           *testing.T
	mu          sync.Mutex
	hits        map[string][]string
	authorities map[string][]originSSHCertificateAuthority
	require     map[string]bool
	nextID      int
}

func newSSHCAMock(t *testing.T) *sshCAMock {
	m := &sshCAMock{t: t, hits: map[string][]string{}, authorities: map[string][]originSSHCertificateAuthority{}, require: map[string]bool{}}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	return m
}

func (m *sshCAMock) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	defer m.mu.Unlock()
	route := r.Method + " " + r.URL.Path
	m.hits[route] = append(m.hits[route], strings.TrimSpace(string(raw)))

	// /owners/{owner}/ssh-certificate-authorities[/{id}] or /owners/{owner}/ssh-certificate-authorities:setRequirement
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/owners/"), "/")
	if len(parts) < 2 || !strings.HasPrefix(parts[1], "ssh-certificate-authorities") {
		http.NotFound(w, r)
		return
	}
	owner, collection := parts[0], parts[1]
	switch {
	case r.Method == http.MethodGet && len(parts) == 2 && collection == "ssh-certificate-authorities":
		writeJSON(m.t, w, http.StatusOK, originSSHCertificateAuthorityList{CertificateAuthorities: m.list(owner), RequireCertificates: m.require[owner]})
	case r.Method == http.MethodPost && len(parts) == 2 && collection == "ssh-certificate-authorities:setRequirement":
		var body struct {
			RequireCertificates *bool `json:"requireCertificates"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.RequireCertificates == nil {
			writeJSON(m.t, w, http.StatusBadRequest, originStatusError{Code: 3, Message: "require_certificates is required"})
			return
		}
		if *body.RequireCertificates && len(m.authorities[owner]) == 0 {
			writeJSON(m.t, w, http.StatusBadRequest, originStatusError{Code: 9, Message: "add a certificate authority before requiring certificates"})
			return
		}
		m.require[owner] = *body.RequireCertificates
		writeJSON(m.t, w, http.StatusOK, originSSHCertificateRequirement{RequireCertificates: m.require[owner]})
	case r.Method == http.MethodPost && len(parts) == 2 && collection == "ssh-certificate-authorities":
		var write originSSHCertificateAuthorityWrite
		if err := json.Unmarshal(raw, &write); err != nil || len(strings.Fields(write.PublicKey)) < 2 || strings.TrimSpace(write.Name) == "" {
			writeJSON(m.t, w, http.StatusBadRequest, originStatusError{Code: 3, Message: "public_key must be a single OpenSSH authorized_keys entry"})
			return
		}
		authority, ok := m.addLocked(owner, write.PublicKey, write.Name)
		if !ok {
			writeJSON(m.t, w, http.StatusConflict, originStatusError{Code: 6, Message: "the owner already lists this key"})
			return
		}
		writeJSON(m.t, w, http.StatusOK, authority)
	case r.Method == http.MethodDelete && len(parts) == 3 && collection == "ssh-certificate-authorities":
		if m.require[owner] && len(m.authorities[owner]) == 1 && m.authorities[owner][0].ID == parts[2] {
			writeJSON(m.t, w, http.StatusBadRequest, originStatusError{Code: 9, Message: "the last certificate authority cannot be removed while certificates are required"})
			return
		}
		for i, authority := range m.authorities[owner] {
			if authority.ID == parts[2] {
				m.authorities[owner] = append(m.authorities[owner][:i], m.authorities[owner][i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		writeJSON(m.t, w, http.StatusNotFound, originStatusError{Code: 5, Message: "not found"})
	default:
		http.NotFound(w, r)
	}
}

// Newest first, like the API.
func (m *sshCAMock) list(owner string) []originSSHCertificateAuthority {
	stored := m.authorities[owner]
	out := make([]originSSHCertificateAuthority, 0, len(stored))
	for i := len(stored) - 1; i >= 0; i-- {
		out = append(out, stored[i])
	}
	return out
}

func (m *sshCAMock) add(owner, publicKey, name string) originSSHCertificateAuthority {
	m.mu.Lock()
	defer m.mu.Unlock()
	authority, ok := m.addLocked(owner, publicKey, name)
	if !ok {
		m.t.Fatalf("duplicate key for %s", owner)
	}
	return authority
}

func (m *sshCAMock) addLocked(owner, publicKey, name string) (originSSHCertificateAuthority, bool) {
	fields := strings.Fields(publicKey)
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		m.t.Fatalf("decode key blob: %v", err)
	}
	sum := sha256.Sum256(blob)
	fingerprint := originSSHFingerprintPrefix + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
	for _, existing := range m.authorities[owner] {
		if existing.Fingerprint == fingerprint {
			return originSSHCertificateAuthority{}, false
		}
	}
	m.nextID++
	authority := originSSHCertificateAuthority{
		ID:          fmt.Sprintf("nsca_%026d", m.nextID),
		Name:        strings.TrimSpace(name),
		KeyType:     fields[0],
		Fingerprint: fingerprint,
		PublicKey:   fields[0] + " " + fields[1],
		CreatedAt:   fmt.Sprintf("2026-08-02T14:45:%02dZ", m.nextID),
	}
	m.authorities[owner] = append(m.authorities[owner], authority)
	return authority, true
}

func (m *sshCAMock) setRequire(owner string, require bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.require[owner] = require
}

func (m *sshCAMock) client() *apiClient {
	return &apiClient{
		httpClient: m.Client(),
		authHeader: "Bearer session-token",
		userAgent:  "terraform-provider-cursor/test",
		originBase: m.URL,
	}
}

func (m *sshCAMock) lastBody(route string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	bodies := m.hits[route]
	if len(bodies) == 0 {
		m.t.Fatalf("no request recorded for %s", route)
	}
	return bodies[len(bodies)-1]
}

func (m *sshCAMock) count(route string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.hits[route])
}
