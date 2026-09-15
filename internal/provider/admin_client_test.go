package provider

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testAdminClient(server *httptest.Server, team, org string) *apiClient {
	return &apiClient{
		httpClient: server.Client(),
		userAgent:  "terraform-provider-cursor/test",
		adminBase:  server.URL,
		teamAPIKey: team,
		orgAPIKey:  org,
	}
}

func TestResolveTeamMemberIDUsesBasicAuthAndPaginates(t *testing.T) {
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/teams/members" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("key_team:"))
		if got := r.Header.Get("Authorization"); got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if r.URL.Query().Get("pageSize") != "100" {
			t.Errorf("pageSize = %q", r.URL.Query().Get("pageSize"))
		}
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		hasNext := page == "1"
		members := []adminTeamMember{{ID: "user_other", Email: "other@acme.com"}}
		if page == "2" {
			members = []adminTeamMember{{ID: "42", PublicID: "user_alice", Email: "Alice@Acme.com", Name: "Alice"}}
		}
		writeJSON(t, w, http.StatusOK, adminTeamMembersResponse{TeamMembers: members, Pagination: &adminPagination{HasNextPage: &hasNext}})
	}))
	defer server.Close()

	id, err := testAdminClient(server, "key_team", "").resolveTeamMemberID(context.Background(), "alice@acme.com")
	if err != nil {
		t.Fatalf("resolveTeamMemberID() error: %v", err)
	}
	if id != "user_alice" {
		t.Fatalf("id = %q, want user_alice from publicId", id)
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Fatalf("pages = %v", pages)
	}
}

func TestResolveTeamMemberIDStopsAtShortPageWithoutPagination(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeJSON(t, w, http.StatusOK, adminTeamMembersResponse{TeamMembers: []adminTeamMember{{ID: "user_bob", Email: "bob@acme.com"}}})
	}))
	defer server.Close()

	id, err := testAdminClient(server, "key_team", "").resolveTeamMemberID(context.Background(), "BOB@acme.com")
	if err != nil || id != "user_bob" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestResolveTeamMemberIDErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, adminTeamMembersResponse{TeamMembers: []adminTeamMember{
			{ID: "user_a", Email: "dup@acme.com"},
			{ID: "user_b", Email: "Dup@acme.com"},
			{ID: "7", Email: "legacy@acme.com"},
		}})
	}))
	defer server.Close()
	client := testAdminClient(server, "key_team", "")

	if _, err := client.resolveTeamMemberID(context.Background(), "nobody@acme.com"); err == nil || !strings.Contains(err.Error(), "no team member has email") {
		t.Fatalf("error = %v, want no match", err)
	}
	if _, err := client.resolveTeamMemberID(context.Background(), "dup@acme.com"); err == nil || !strings.Contains(err.Error(), "2 team members") {
		t.Fatalf("error = %v, want ambiguity", err)
	}
	if _, err := client.resolveTeamMemberID(context.Background(), "legacy@acme.com"); err == nil || !strings.Contains(err.Error(), "no user_ identifier") {
		t.Fatalf("error = %v, want missing user_ id", err)
	}
	if _, err := testAdminClient(server, "", "").resolveTeamMemberID(context.Background(), "a@acme.com"); err == nil || !strings.Contains(err.Error(), "team_api_key is required") {
		t.Fatalf("error = %v, want missing key", err)
	}
}

func TestResolveTeamMemberIDRejectedKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := testAdminClient(server, "key_bad", "").resolveTeamMemberID(context.Background(), "a@acme.com")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "team") {
		t.Fatalf("error = %v, want rejected team key", err)
	}
}

// Serves the Org Admin list endpoint with its exact-match ?name= filter (miss = empty groups, not 404).
func orgGroupsServer(t *testing.T, groups []adminGroup) (*httptest.Server, *[]string) {
	t.Helper()
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/organizations/groups" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("key_org:"))
		if got := r.Header.Get("Authorization"); got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		queries = append(queries, r.URL.RawQuery)
		matches := []adminGroup{}
		for _, group := range groups {
			if group.Name == r.URL.Query().Get("name") {
				matches = append(matches, group)
			}
		}
		writeJSON(t, w, http.StatusOK, adminGroupsResponse{Groups: matches})
	}))
	return server, &queries
}

func TestResolveOrganizationGroupIDUsesNameFilterAndPublicID(t *testing.T) {
	server, queries := orgGroupsServer(t, []adminGroup{
		{ID: "g_1", PublicID: "grp_eng", Name: "Engineering"},
		{ID: "g_2", PublicID: "grp_eng_lower", Name: "engineering"},
		{ID: "g_3", Name: "Legacy"},
		{ID: "g_4", PublicID: "g_4", Name: "Broken"},
		{ID: "g_5", PublicID: "grp_dup1", Name: "Dup"},
		{ID: "g_6", PublicID: "grp_dup2", Name: "Dup"},
		{ID: "g_7", PublicID: "grp_core", Name: "Platform / Core & Infra"},
	})
	defer server.Close()
	client := testAdminClient(server, "", "key_org")

	id, err := client.resolveOrganizationGroupID(context.Background(), "Engineering")
	if err != nil || id != "grp_eng" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	if len(*queries) != 1 || (*queries)[0] != "name=Engineering" {
		t.Fatalf("queries = %v, want a single ?name=Engineering list call", *queries)
	}
	id, err = client.resolveOrganizationGroupID(context.Background(), "Platform / Core & Infra")
	if err != nil || id != "grp_core" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	if last := (*queries)[len(*queries)-1]; last != "name=Platform+%2F+Core+%26+Infra" {
		t.Fatalf("query = %q, want the name URL-encoded", last)
	}
	if _, err := client.resolveOrganizationGroupID(context.Background(), "ENGINEERING"); err == nil || !strings.Contains(err.Error(), "no organization group is named") {
		t.Fatalf("error = %v, want exact-match miss on empty list", err)
	}
	if _, err := client.resolveOrganizationGroupID(context.Background(), "Legacy"); err == nil || !strings.Contains(err.Error(), "has no publicId") {
		t.Fatalf("error = %v, want missing publicId", err)
	}
	if _, err := client.resolveOrganizationGroupID(context.Background(), "Broken"); err == nil || !strings.Contains(err.Error(), "does not start with grp_") {
		t.Fatalf("error = %v, want bad publicId prefix", err)
	}
	if _, err := client.resolveOrganizationGroupID(context.Background(), "Dup"); err == nil || !strings.Contains(err.Error(), "returned 2 groups") {
		t.Fatalf("error = %v, want ambiguity", err)
	}
	if _, err := testAdminClient(server, "", "").resolveOrganizationGroupID(context.Background(), "Engineering"); err == nil || !strings.Contains(err.Error(), "organization_api_key is required") {
		t.Fatalf("error = %v, want missing key", err)
	}
}

func TestResolveOrganizationGroupIDRejectsUnfilteredResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, adminGroupsResponse{Groups: []adminGroup{{ID: "g_1", PublicID: "grp_other", Name: "Other"}}})
	}))
	defer server.Close()

	_, err := testAdminClient(server, "", "key_org").resolveOrganizationGroupID(context.Background(), "Engineering")
	if err == nil || !strings.Contains(err.Error(), "filter was not applied") {
		t.Fatalf("error = %v, want unfiltered-response guard", err)
	}
}

func TestAdminPagingCapsRunawayServers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasNext := true
		writeJSON(t, w, http.StatusOK, adminTeamMembersResponse{TeamMembers: []adminTeamMember{{ID: "user_x", Email: "x@acme.com"}}, Pagination: &adminPagination{HasNextPage: &hasNext}})
	}))
	defer server.Close()

	_, err := testAdminClient(server, "key_team", "").resolveTeamMemberID(context.Background(), "nope@acme.com")
	if err == nil || !strings.Contains(err.Error(), "more than 100 pages") {
		t.Fatalf("error = %v, want page cap", err)
	}
}
