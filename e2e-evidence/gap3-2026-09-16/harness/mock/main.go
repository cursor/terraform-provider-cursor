// mockcursor is a self-contained, test-only stand-in for https://api.cursor.com used to exercise
// terraform-provider-cursor end to end without a live Origin deployment.
//
// It serves, on one TLS listener whose certificate is valid for api.cursor.com:
//   - the public Origin API under /v1/origin (repos, repo/owner grants, repo rulesets)
//   - the Team Admin API      GET /teams/members            (HTTP Basic, team key)
//   - the Organization Admin  GET /organizations/groups     (HTTP Basic, org key)
//
// The provider hardcodes https://api.cursor.com, so a plain-HTTP CONNECT proxy is also started;
// pointing HTTPS_PROXY at it tunnels every provider request into the TLS listener. The generated
// CA is written to -ca-out so SSL_CERT_FILE can trust it.
//
// Every request/response is appended to -trace with credentials redacted. The mock enforces the
// wire contract that matters for grants: Origin grant bodies must carry user_ / grp_ IDs (or a
// teamGroup kind) and never an email, a group name, or an internal g_ id.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type member struct {
	ID        string `json:"id"`
	PublicID  string `json:"publicId"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	IsRemoved bool   `json:"isRemoved"`
}

type group struct {
	ID       string `json:"id"`
	PublicID string `json:"publicId"`
	Name     string `json:"name"`
}

type grant struct {
	User *struct {
		ID string `json:"id"`
	} `json:"user,omitempty"`
	Group *struct {
		ID string `json:"id"`
	} `json:"group,omitempty"`
	TeamGroup *struct {
		Kind string `json:"kind"`
	} `json:"teamGroup,omitempty"`
	Permission string `json:"permission,omitempty"`
}

func (g grant) key() string {
	switch {
	case g.User != nil:
		return "user:" + g.User.ID
	case g.Group != nil:
		return "group:" + g.Group.ID
	case g.TeamGroup != nil:
		return "team_group:" + g.TeamGroup.Kind
	}
	return ""
}

type rule struct {
	ID         string          `json:"id"`
	RuleType   string          `json:"ruleType"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

type bypassActor struct {
	ID         string          `json:"id"`
	BypassMode string          `json:"bypassMode"`
	User       json.RawMessage `json:"user,omitempty"`
	Team       json.RawMessage `json:"team,omitempty"`
	App        json.RawMessage `json:"app,omitempty"`
	OriginRole json.RawMessage `json:"originRole,omitempty"`
}

type ruleset struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Description      string        `json:"description"`
	Enforcement      string        `json:"enforcement"`
	Kind             string        `json:"kind"`
	IncludedRefNames []string      `json:"includedRefNames"`
	ExcludedRefNames []string      `json:"excludedRefNames"`
	Rules            []rule        `json:"rules"`
	BypassActors     []bypassActor `json:"bypassActors"`
}

type server struct {
	mu          sync.Mutex
	seq         int
	trace       io.Writer
	token       string
	teamKey     string
	orgKey      string
	members     []member
	groups      []group
	repoGrants  map[string][]grant // owner/repo -> grants
	ownerGrants map[string][]grant // owner -> grants
	rulesets    map[string]map[string]*ruleset
	nextID      int
	violations  int
}

func newServer(trace io.Writer, token, teamKey, orgKey string) *server {
	return &server{
		trace:   trace,
		token:   token,
		teamKey: teamKey,
		orgKey:  orgKey,
		members: []member{
			{ID: "1", PublicID: "user_01k2ja2000e0080000000000a1", Email: "alice@acme.com", Name: "Alice"},
			{ID: "2", PublicID: "user_01k2ja2000e0080000000000b2", Email: "bob@acme.com", Name: "Bob"},
			// Removed member sharing Bob's email: the resolver must skip it.
			{ID: "3", PublicID: "user_01k2ja2000e0080000000000b9", Email: "bob@acme.com", Name: "Bob (old seat)", IsRemoved: true},
			{ID: "4", PublicID: "user_01k2ja2000e0080000000000c3", Email: "carol@acme.com", Name: "Carol"},
			{ID: "5", PublicID: "user_01k2ja2000e0080000000000d4", Email: "dave@acme.com", Name: "Dave"},
			{ID: "6", PublicID: "user_01k2ja2000e0080000000000e5", Email: "erin@acme.com", Name: "Erin"},
			{ID: "7", PublicID: "user_01k2ja2000e0080000000000f6", Email: "frank@acme.com", Name: "Frank"},
		},
		groups: []group{
			{ID: "g_1001", PublicID: "grp_01k2ja2000e0080000000000g1", Name: "Engineering"},
			{ID: "g_1002", PublicID: "grp_01k2ja2000e0080000000000g2", Name: "Security"},
			{ID: "g_1003", PublicID: "grp_01k2ja2000e0080000000000g3", Name: "engineering"}, // case differs on purpose
		},
		repoGrants:  map[string][]grant{},
		ownerGrants: map[string][]grant{},
		rulesets:    map[string]map[string]*ruleset{},
	}
}

// ---- trace ---------------------------------------------------------------------------------

type recorder struct {
	http.ResponseWriter
	status int
	body   strings.Builder
}

func (r *recorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

func (s *server) authLabel(r *http.Request) string {
	h := r.Header.Get("Authorization")
	switch {
	case h == "":
		return "none"
	case strings.HasPrefix(h, "Bearer "):
		if strings.TrimPrefix(h, "Bearer ") == s.token {
			return "Bearer(<provider token, redacted>)"
		}
		return "Bearer(<UNEXPECTED TOKEN>)"
	case strings.HasPrefix(h, "Basic "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "Basic "))
		if err != nil {
			return "Basic(<undecodable>)"
		}
		user, pass, _ := strings.Cut(string(raw), ":")
		suffix := ""
		if pass != "" {
			suffix = ", NON-EMPTY PASSWORD"
		}
		switch user {
		case s.teamKey:
			return "Basic(team_api_key as username, empty password" + suffix + ")"
		case s.orgKey:
			return "Basic(organization_api_key as username, empty password" + suffix + ")"
		}
		return "Basic(<UNKNOWN KEY>" + suffix + ")"
	}
	return "<unrecognised scheme>"
}

func (s *server) logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.trace, format, args...)
}

func (s *server) traced(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		s.mu.Lock()
		s.seq++
		n := s.seq
		s.mu.Unlock()
		rec := &recorder{ResponseWriter: w}
		next(rec, r)
		reqBody := strings.TrimSpace(string(body))
		if reqBody == "" {
			reqBody = "<empty>"
		}
		respBody := strings.TrimSpace(rec.body.String())
		if respBody == "" {
			respBody = "<empty>"
		}
		s.logf("#%03d %s %s https://%s%s\n     auth: %s\n     user-agent: %s\n     request:  %s\n     response: %d %s\n\n",
			n, time.Now().UTC().Format("15:04:05.000"), r.Method, r.Host, r.URL.RequestURI(),
			s.authLabel(r), r.Header.Get("User-Agent"), reqBody, rec.status, respBody)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func originError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"code": status, "message": msg})
}

// ---- handlers ------------------------------------------------------------------------------

func (s *server) requireBearer(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		originError(w, http.StatusUnauthorized, "missing or invalid bearer token")
		return false
	}
	return true
}

func (s *server) requireBasic(w http.ResponseWriter, r *http.Request, key string) bool {
	user, pass, ok := r.BasicAuth()
	if !ok || user != key || pass != "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid API key"})
		return false
	}
	return true
}

func (s *server) teamMembers(w http.ResponseWriter, r *http.Request) {
	if !s.requireBasic(w, r, s.teamKey) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Documented behaviour: one unpaginated response, no pagination object.
	writeJSON(w, http.StatusOK, map[string]any{"teamMembers": s.members})
}

func (s *server) orgGroups(w http.ResponseWriter, r *http.Request) {
	if !s.requireBasic(w, r, s.orgKey) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := r.URL.Query().Get("name")
	out := []group{}
	for _, g := range s.groups {
		if name == "" || g.Name == name {
			out = append(out, g)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out, "pagination": map[string]any{"hasNextPage": false}})
}

func (s *server) repo(w http.ResponseWriter, r *http.Request, owner, name string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            "repo_01k2ja2000e0080000000000r1",
		"name":          name,
		"fullName":      owner + "/" + name,
		"owner":         map[string]string{"slug": owner, "id": "team_01k2ja2000e0080000000000t1", "type": "team"},
		"defaultBranch": "main",
		"cloneUrl":      "https://origin.cursor.com/" + owner + "/" + name + ".git",
		"visibility":    "private",
		"createdAt":     "2026-01-01T00:00:00Z",
		"updatedAt":     "2026-01-02T00:00:00Z",
	})
}

// validateGrant enforces what Origin accepts on the wire. A body containing an email address,
// a human group name, or an internal g_ id is a contract violation and is rejected with 400.
func (s *server) validateGrant(g grant, raw []byte, ownerScope bool) error {
	set := 0
	if g.User != nil {
		set++
		if !strings.HasPrefix(g.User.ID, "user_") {
			return fmt.Errorf("user.id %q must be a user_ TypeID", g.User.ID)
		}
	}
	if g.Group != nil {
		set++
		if !strings.HasPrefix(g.Group.ID, "grp_") {
			return fmt.Errorf("group.id %q must be a grp_ public id (g_ internal ids are rejected)", g.Group.ID)
		}
	}
	if g.TeamGroup != nil {
		set++
		if g.TeamGroup.Kind != "members" && g.TeamGroup.Kind != "admins" {
			return fmt.Errorf("teamGroup.kind %q must be members or admins", g.TeamGroup.Kind)
		}
	}
	if set != 1 {
		return fmt.Errorf("exactly one principal is required, got %d", set)
	}
	if strings.Contains(string(raw), "@") {
		return fmt.Errorf("grant body contains an email address; principals must be resolved to user_ ids before reaching Origin")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, grp := range s.groups {
		if strings.Contains(string(raw), `"`+grp.Name+`"`) || strings.Contains(string(raw), `"`+grp.ID+`"`) {
			return fmt.Errorf("grant body contains group name or internal id %q; only publicId grp_ is accepted", grp.Name)
		}
	}
	return nil
}

func (s *server) grants(w http.ResponseWriter, r *http.Request, store map[string][]grant, key string, ownerScope bool) {
	if !s.requireBearer(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
		if page < 1 {
			page = 1
		}
		if size < 1 {
			size = 100
		}
		s.mu.Lock()
		all := store[key]
		s.mu.Unlock()
		start := (page - 1) * size
		out := []grant{}
		if start < len(all) {
			end := start + size
			if end > len(all) {
				end = len(all)
			}
			out = append(out, all[start:end]...)
		}
		writeJSON(w, http.StatusOK, map[string]any{"grants": out})
	case http.MethodPost, http.MethodDelete:
		raw, _ := io.ReadAll(r.Body)
		var g grant
		if err := json.Unmarshal(raw, &g); err != nil {
			originError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := s.validateGrant(g, raw, ownerScope); err != nil {
			s.mu.Lock()
			s.violations++
			s.mu.Unlock()
			originError(w, http.StatusBadRequest, "WIRE CONTRACT VIOLATION: "+err.Error())
			return
		}
		if r.Method == http.MethodPost {
			if g.Permission == "" {
				originError(w, http.StatusBadRequest, "permission is required")
				return
			}
			if ownerScope && !strings.HasPrefix(g.Permission, "PERMISSION_") {
				originError(w, http.StatusBadRequest, "namespace grants use PERMISSION_* enum names")
				return
			}
			if !ownerScope && strings.HasPrefix(g.Permission, "PERMISSION_") {
				originError(w, http.StatusBadRequest, "repository grants use bare permission names")
				return
			}
			s.mu.Lock()
			replaced := false
			for i := range store[key] {
				if store[key][i].key() == g.key() {
					store[key][i] = g
					replaced = true
				}
			}
			if !replaced {
				store[key] = append(store[key], g)
			}
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, g)
			return
		}
		s.mu.Lock()
		kept := store[key][:0:0]
		found := false
		for _, existing := range store[key] {
			if existing.key() == g.key() {
				found = true
				continue
			}
			kept = append(kept, existing)
		}
		store[key] = kept
		s.mu.Unlock()
		if !found {
			originError(w, http.StatusNotFound, "no such grant")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		originError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *server) newID(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s_01k2ja2000e00800000000%04d", prefix, s.nextID)
}

func (s *server) rulesetCollection(w http.ResponseWriter, r *http.Request, repoKey string) {
	if !s.requireBearer(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		originError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var rs ruleset
	if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
		originError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rs.ID = s.newID("rs")
	s.assignRulesetIDs(&rs)
	if s.rulesets[repoKey] == nil {
		s.rulesets[repoKey] = map[string]*ruleset{}
	}
	s.rulesets[repoKey][rs.ID] = &rs
	writeJSON(w, http.StatusCreated, rs)
}

func (s *server) assignRulesetIDs(rs *ruleset) {
	if rs.IncludedRefNames == nil {
		rs.IncludedRefNames = []string{}
	}
	if rs.ExcludedRefNames == nil {
		rs.ExcludedRefNames = []string{}
	}
	if rs.Rules == nil {
		rs.Rules = []rule{}
	}
	if rs.BypassActors == nil {
		rs.BypassActors = []bypassActor{}
	}
	for i := range rs.Rules {
		rs.Rules[i].ID = s.newID("rule")
	}
	for i := range rs.BypassActors {
		rs.BypassActors[i].ID = s.newID("ba")
	}
}

func (s *server) rulesetItem(w http.ResponseWriter, r *http.Request, repoKey, id string) {
	if !s.requireBearer(w, r) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.rulesets[repoKey][id]
	if existing == nil {
		originError(w, http.StatusNotFound, "ruleset not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, existing)
	case http.MethodPut:
		var rs ruleset
		if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
			originError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		rs.ID = id
		s.assignRulesetIDs(&rs)
		s.rulesets[repoKey][id] = &rs
		writeJSON(w, http.StatusOK, rs)
	case http.MethodDelete:
		delete(s.rulesets[repoKey], id)
		w.WriteHeader(http.StatusNoContent)
	default:
		originError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *server) route(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case p == "/teams/members":
		s.teamMembers(w, r)
	case p == "/organizations/groups":
		s.orgGroups(w, r)
	case strings.HasPrefix(p, "/v1/origin/"):
		parts := strings.Split(strings.TrimPrefix(p, "/v1/origin/"), "/")
		for i := range parts {
			parts[i], _ = url.PathUnescape(parts[i])
		}
		switch {
		case len(parts) == 3 && parts[0] == "repos":
			if !s.requireBearer(w, r) {
				return
			}
			s.repo(w, r, parts[1], parts[2])
		case len(parts) == 4 && parts[0] == "repos" && parts[3] == "grants":
			s.grants(w, r, s.repoGrants, parts[1]+"/"+parts[2], false)
		case len(parts) == 3 && parts[0] == "owners" && parts[2] == "grants":
			s.grants(w, r, s.ownerGrants, parts[1], true)
		case len(parts) == 4 && parts[0] == "repos" && parts[3] == "rulesets":
			s.rulesetCollection(w, r, parts[1]+"/"+parts[2])
		case len(parts) == 5 && parts[0] == "repos" && parts[3] == "rulesets":
			s.rulesetItem(w, r, parts[1]+"/"+parts[2], parts[4])
		default:
			originError(w, http.StatusNotFound, "no such Origin route")
		}
	default:
		http.NotFound(w, r)
	}
}

// ---- control endpoints (plain HTTP, same port as the proxy) --------------------------------

func (s *server) control(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/_mock/state":
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"repoGrants": s.repoGrants, "ownerGrants": s.ownerGrants, "rulesets": s.rulesets,
			"members": s.members, "groups": s.groups, "wireViolations": s.violations,
		})
	case r.URL.Path == "/_mock/members" && r.Method == http.MethodPost:
		var m []member
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.members = m
		s.mu.Unlock()
		s.logf("---- control: team directory replaced (%d members) ----\n\n", len(m))
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/_mock/mark" && r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		s.logf("==== %s ====\n\n", strings.TrimSpace(string(raw)))
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

// ---- CONNECT proxy -------------------------------------------------------------------------

func (s *server) proxy(tlsAddr string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			s.control(w, r)
			return
		}
		s.logf("     [proxy] CONNECT %s -> tunnelled to mock TLS listener %s\n\n", r.Host, tlsAddr)
		upstream, err := net.Dial("tcp", tlsAddr)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		client, _, err := hj.Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go func() { defer upstream.Close(); _, _ = io.Copy(upstream, client) }()
		go func() { defer client.Close(); _, _ = io.Copy(client, upstream) }()
	}
}

// ---- TLS material --------------------------------------------------------------------------

func newCA() (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gap3 e2e mock CA (test only)"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	return cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func newLeaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, hosts ...string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     hosts,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: key}, nil
}

func main() {
	tracePath := flag.String("trace", "http-trace.log", "HTTP trace output file")
	caOut := flag.String("ca-out", "ca.pem", "where to write the test CA certificate")
	proxyAddr := flag.String("proxy-addr", "127.0.0.1:18443", "plain-HTTP CONNECT proxy + /_mock control listener")
	token := flag.String("token", "", "expected provider bearer token")
	teamKey := flag.String("team-key", "", "expected Team Admin API key")
	orgKey := flag.String("org-key", "", "expected Organization Admin API key")
	flag.Parse()
	if *token == "" || *teamKey == "" || *orgKey == "" {
		log.Fatal("-token, -team-key and -org-key are required")
	}

	traceFile, err := os.OpenFile(*tracePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	defer traceFile.Close()
	s := newServer(traceFile, *token, *teamKey, *orgKey)

	ca, caKey, caPEM, err := newCA()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*caOut, caPEM, 0o644); err != nil {
		log.Fatal(err)
	}
	leaf, err := newLeaf(ca, caKey, "api.cursor.com", "api2.cursor.sh", "localhost")
	if err != nil {
		log.Fatal(err)
	}
	tlsLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12})
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		log.Fatal(http.Serve(tlsLn, s.traced(s.route)))
	}()

	fmt.Fprintf(traceFile, "mockcursor started %s; TLS listener %s presents a cert for api.cursor.com signed by the test CA\n\n", time.Now().UTC().Format(time.RFC3339), tlsLn.Addr())
	log.Printf("mock TLS api.cursor.com at %s; CONNECT proxy + control at http://%s; CA at %s", tlsLn.Addr(), *proxyAddr, *caOut)
	log.Fatal(http.ListenAndServe(*proxyAddr, s.proxy(tlsLn.Addr().String())))
}
