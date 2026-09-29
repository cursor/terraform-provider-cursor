package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOriginGrantListFollowsPageTokensPastShortPages(t *testing.T) {
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/rocket/grants" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if got := query.Get("pageSize"); got != "100" {
			t.Errorf("pageSize = %q", got)
		}
		if query.Has("page") {
			t.Errorf("page = %q; the API pages with pageToken", query.Get("page"))
		}
		token := query.Get("pageToken")
		tokens = append(tokens, token)
		switch token {
		case "":
			writeJSON(t, w, http.StatusOK, originGrantList{Grants: sampleUserGrants(2), NextPageToken: "p2+/="})
		case "p2+/=":
			writeJSON(t, w, http.StatusOK, originGrantList{NextPageToken: "p3"})
		case "p3":
			writeJSON(t, w, http.StatusOK, originGrantList{Grants: []originGrant{{
				TeamGroup:  &originGrantTeamGroup{Kind: originTeamGroupAdmins},
				Permission: originPermissionAdmin,
			}}})
		default:
			t.Errorf("unexpected pageToken %q", token)
			writeJSON(t, w, http.StatusOK, originGrantList{})
		}
	}))
	defer server.Close()

	grants, err := testOriginClient(server).listOriginGrants(context.Background(), originRepoGrantsPath("acme", "rocket"))
	if err != nil {
		t.Fatalf("listOriginGrants() error: %v", err)
	}
	if strings.Join(tokens, ",") != ",p2+/=,p3" {
		t.Fatalf("pageTokens sent = %q, want none, then p2+/=, then p3", tokens)
	}
	if len(grants) != 3 {
		t.Fatalf("len(grants) = %d, want 3", len(grants))
	}
	if grants[2].TeamGroup == nil || grants[2].TeamGroup.Kind != originTeamGroupAdmins {
		t.Fatalf("last grant = %#v", grants[2])
	}
}

func TestOriginGrantListStopsAtPageLimit(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeJSON(t, w, http.StatusOK, originGrantList{NextPageToken: fmt.Sprintf("p%d", requests)})
	}))
	defer server.Close()

	_, err := testOriginClient(server).listOriginGrants(context.Background(), originOwnerGrantsPath("acme"))
	if err == nil || !strings.Contains(err.Error(), "more than 100 pages") {
		t.Fatalf("error = %v, want page limit", err)
	}
	if requests != maxOriginGrantPages {
		t.Fatalf("requests = %d, want %d", requests, maxOriginGrantPages)
	}
}

func TestOriginGrantListEmptyCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, originGrantList{})
	}))
	defer server.Close()

	grants, err := testOriginClient(server).listOriginGrants(context.Background(), originOwnerGrantsPath("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if grants == nil || len(grants) != 0 {
		t.Fatalf("grants = %#v, want empty non-nil slice", grants)
	}
}

func TestOriginGrantFindStopsAtMatchingPage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if token := r.URL.Query().Get("pageToken"); token != "" {
			t.Errorf("unexpected pageToken %q", token)
		}
		grants := sampleUserGrants(100)
		grants[7] = originGrant{Group: &originGrantGroup{ID: "grp_01"}, Permission: originPermissionWrite}
		writeJSON(t, w, http.StatusOK, originGrantList{Grants: grants, NextPageToken: "p2"})
	}))
	defer server.Close()

	found, err := testOriginClient(server).findOriginGrant(context.Background(), originRepoGrantsPath("acme", "rocket"), originGrantKey{kind: originGrantKindGroup, value: "grp_01"})
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.Group == nil || found.Permission != originPermissionWrite {
		t.Fatalf("found = %#v", found)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestOriginGrantFindMissingReturnsNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, originGrantList{Grants: sampleUserGrants(3)})
	}))
	defer server.Close()

	found, err := testOriginClient(server).findOriginGrant(context.Background(), originRepoGrantsPath("acme", "rocket"), originGrantKey{kind: originGrantKindUser, value: "user_missing"})
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Fatalf("found = %#v, want nil", found)
	}
}

func TestOriginGrantFindPropagatesNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, originStatusError{Code: 5, Message: "missing"})
	}))
	defer server.Close()

	_, err := testOriginClient(server).findOriginGrant(context.Background(), originRepoGrantsPath("acme", "gone"), originGrantKey{kind: originGrantKindUser, value: "user_01"})
	if !isOriginNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
}

func TestOriginGrantUpsertAndDeleteBodies(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer session-token" {
			t.Errorf("Authorization = %q", got)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(raw)))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/rocket/grants":
			writeJSON(t, w, http.StatusOK, originGrant{User: &originGrantUser{ID: "user_01"}, Permission: originPermissionWrite})
		case r.Method == http.MethodPost && r.URL.Path == "/namespaces/acme/grants":
			writeJSON(t, w, http.StatusCreated, originGrant{TeamGroup: &originGrantTeamGroup{Kind: originTeamGroupAdmins}, Permission: "PERMISSION_ADMIN"})
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/acme/rocket/grants":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := testOriginClient(server)
	ctx := context.Background()
	got, err := client.upsertOriginGrant(ctx, originRepoGrantsPath("acme", "rocket"), originGrant{User: &originGrantUser{ID: "user_01"}, Permission: originPermissionWrite})
	if err != nil {
		t.Fatalf("upsertOriginGrant() error: %v", err)
	}
	if got.User == nil || got.User.ID != "user_01" || got.Permission != originPermissionWrite {
		t.Fatalf("upsert result = %#v", got)
	}

	got, err = client.upsertOriginGrant(ctx, originOwnerGrantsPath("acme"), originGrant{TeamGroup: &originGrantTeamGroup{Kind: originTeamGroupAdmins}, Permission: ownerPermissionToWire(originPermissionAdmin)})
	if err != nil {
		t.Fatalf("upsertOriginGrant() error: %v", err)
	}
	if got.TeamGroup == nil || got.Permission != "PERMISSION_ADMIN" {
		t.Fatalf("upsert result = %#v", got)
	}

	if err := client.deleteOriginGrant(ctx, originRepoGrantsPath("acme", "rocket"), originGrantKey{kind: originGrantKindUser, value: "user_01"}); err != nil {
		t.Fatalf("deleteOriginGrant() error: %v", err)
	}

	want := []string{
		`POST /repos/acme/rocket/grants {"user":{"id":"user_01"},"permission":"write"}`,
		`POST /namespaces/acme/grants {"teamGroup":{"kind":"admins"},"permission":"PERMISSION_ADMIN"}`,
		`DELETE /repos/acme/rocket/grants {"user":{"id":"user_01"}}`,
	}
	if strings.Join(bodies, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(bodies, "\n"), strings.Join(want, "\n"))
	}
}

func TestOriginGrantUpsertEmptyResponseUsesRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	requested := originGrant{Group: &originGrantGroup{ID: "grp_01"}, Permission: originPermissionRead}
	got, err := testOriginClient(server).upsertOriginGrant(context.Background(), originRepoGrantsPath("acme", "rocket"), requested)
	if err != nil {
		t.Fatal(err)
	}
	if got.Group == nil || got.Group.ID != "grp_01" || got.Permission != originPermissionRead {
		t.Fatalf("got = %#v", got)
	}
}

func TestOriginGrantUpsertRejectsForeignPrincipal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, originGrant{User: &originGrantUser{ID: "user_other"}, Permission: originPermissionRead})
	}))
	defer server.Close()

	_, err := testOriginClient(server).upsertOriginGrant(context.Background(), originRepoGrantsPath("acme", "rocket"), originGrant{User: &originGrantUser{ID: "user_01"}, Permission: originPermissionRead})
	if err == nil || !strings.Contains(err.Error(), "user:user_other") {
		t.Fatalf("error = %v, want principal mismatch", err)
	}
}

func TestOriginGrantUpsertRequiresPrincipalAndPermission(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	client := testOriginClient(server)
	if _, err := client.upsertOriginGrant(context.Background(), "/namespaces/acme/grants", originGrant{Permission: originPermissionRead}); err == nil || !strings.Contains(err.Error(), "exactly one principal") {
		t.Fatalf("error = %v, want principal error", err)
	}
	if _, err := client.upsertOriginGrant(context.Background(), "/namespaces/acme/grants", originGrant{User: &originGrantUser{ID: "user_01"}}); err == nil || !strings.Contains(err.Error(), "permission is required") {
		t.Fatalf("error = %v, want permission error", err)
	}
}

func TestOriginGrantDeleteNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	err := testOriginClient(server).deleteOriginGrant(context.Background(), originOwnerGrantsPath("acme"), originGrantKey{kind: originGrantKindGroup, value: "grp_01"})
	if !isOriginNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
}

func TestOriginGrantKeyFromWire(t *testing.T) {
	key, err := originGrantKeyFromWire(originGrant{TeamGroup: &originGrantTeamGroup{Kind: originTeamGroupMembers}})
	if err != nil || key != (originGrantKey{kind: originGrantKindTeamGroup, value: originTeamGroupMembers}) {
		t.Fatalf("key = %v, err = %v", key, err)
	}
	if _, err := originGrantKeyFromWire(originGrant{Permission: originPermissionRead}); err == nil {
		t.Fatal("expected error for grant without principal")
	}
	if _, err := originGrantKeyFromWire(originGrant{User: &originGrantUser{ID: "user_01"}, Group: &originGrantGroup{ID: "grp_01"}}); err == nil {
		t.Fatal("expected error for grant with two principals")
	}
	if _, err := originGrantKeyFromWire(originGrant{User: &originGrantUser{}}); err == nil {
		t.Fatal("expected error for user without id")
	}
}

func TestOriginGrantIDRoundTrip(t *testing.T) {
	cases := map[string]originGrantKey{
		"acme/rocket:user:user_01k2ja2000e0080000000000u1": {kind: originGrantKindUser, value: "user_01k2ja2000e0080000000000u1"},
		"acme/rocket:group:grp_01":                         {kind: originGrantKindGroup, value: "grp_01"},
		"acme/rocket:team_group:admins":                    {kind: originGrantKindTeamGroup, value: originTeamGroupAdmins},
		"acme:team_group:members":                          {kind: originGrantKindTeamGroup, value: originTeamGroupMembers},
	}
	for id, key := range cases {
		resource, kind, value, err := parseOriginGrantImportID(id)
		if err != nil {
			t.Fatalf("parseOriginGrantImportID(%q) error: %v", id, err)
		}
		got := originGrantKey{kind: kind, value: value}
		if got != key {
			t.Fatalf("parseOriginGrantImportID(%q) = %v, want %v", id, got, key)
		}
		if rebuilt := originGrantID(resource, got); rebuilt != id {
			t.Fatalf("originGrantID = %q, want %q", rebuilt, id)
		}
	}

	resource, kind, value, err := parseOriginGrantImportID("acme/rocket:user_email:Alice@Acme.com")
	if err != nil || resource != "acme/rocket" || kind != principalFamilyUserEmail || value != "Alice@Acme.com" {
		t.Fatalf("user_email import = %q %q %q, %v", resource, kind, value, err)
	}
	resource, kind, value, err = parseOriginGrantImportID("acme:group_name:Platform: Core")
	if err != nil || resource != "acme" || kind != principalFamilyGroupName || value != "Platform: Core" {
		t.Fatalf("group_name import = %q %q %q, %v", resource, kind, value, err)
	}

	for _, id := range []string{"acme/rocket:user", "acme/rocket:user:", ":user:user_01", "acme/rocket:app:app_01", "acme/rocket:team_group:everyone", " acme:user:user_01"} {
		if _, _, _, err := parseOriginGrantImportID(id); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", id)) {
			t.Fatalf("parseOriginGrantImportID(%q) error = %v, want error naming the ID", id, err)
		}
	}

	owner, repo, err := splitOriginRepoResource("acme/rocket")
	if err != nil || owner != "acme" || repo != "rocket" {
		t.Fatalf("splitOriginRepoResource = %s/%s, %v", owner, repo, err)
	}
	for _, resource := range []string{"acme", "acme/rocket/extra", "/rocket", "acme/"} {
		if _, _, err := splitOriginRepoResource(resource); err == nil {
			t.Fatalf("splitOriginRepoResource(%q) expected error", resource)
		}
	}
}

func TestOwnerPermissionWireMapping(t *testing.T) {
	for tf, wire := range map[string]string{
		originPermissionRead:        "PERMISSION_READ",
		originPermissionContributor: "PERMISSION_CONTRIBUTOR",
		originPermissionWrite:       "PERMISSION_WRITE",
		originPermissionAdmin:       "PERMISSION_ADMIN",
	} {
		if got := ownerPermissionToWire(tf); got != wire {
			t.Errorf("ownerPermissionToWire(%q) = %q, want %q", tf, got, wire)
		}
		if got := ownerPermissionFromWire(wire); got != tf {
			t.Errorf("ownerPermissionFromWire(%q) = %q, want %q", wire, got, tf)
		}
	}
	if got := ownerPermissionFromWire("PERMISSION_CUSTOM"); got != originPermissionCustom {
		t.Errorf("custom = %q", got)
	}
	if got := repoPermissionFromWire(" READ "); got != originPermissionRead {
		t.Errorf("repoPermissionFromWire = %q", got)
	}
}

func sampleUserGrants(n int) []originGrant {
	grants := make([]originGrant, 0, n)
	for i := 0; i < n; i++ {
		grants = append(grants, originGrant{
			User:       &originGrantUser{ID: fmt.Sprintf("user_%03d", i)},
			Permission: originPermissionRead,
		})
	}
	return grants
}
