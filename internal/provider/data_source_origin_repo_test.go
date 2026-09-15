package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	datasourceschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestGetOriginRepoByOwnerAndName(t *testing.T) {
	const token = "Bearer session-token"
	repo := sampleOriginRepo()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/repos/acme/rocket" {
			t.Errorf("path = %s, want /repos/acme/rocket", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != token {
			t.Errorf("Authorization = %q, want %q", got, token)
		}
		if got := r.Header.Get("User-Agent"); got != "terraform-provider-cursor/test" {
			t.Errorf("User-Agent = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		writeJSON(t, w, http.StatusOK, repo)
	}))
	defer server.Close()

	got, err := testOriginClient(server).getOriginRepo(context.Background(), "acme", "rocket")
	if err != nil {
		t.Fatalf("getOriginRepo() error: %v", err)
	}
	if got.ID != repo.ID || got.Name != repo.Name || got.Owner.Slug != "acme" {
		t.Fatalf("getOriginRepo() = %+v, want id/name/owner from sample", got)
	}
	if got.Mirror == nil || got.Mirror.Status != "inbound" {
		t.Fatalf("mirror = %+v, want inbound github mirror", got.Mirror)
	}
}

func TestGetOriginRepoByID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/_/repo_01rocket" {
			t.Errorf("path = %s, want /repos/_/repo_01rocket", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, sampleOriginRepo())
	}))
	defer server.Close()

	got, err := testOriginClient(server).getOriginRepo(context.Background(), "_", "repo_01rocket")
	if err != nil {
		t.Fatalf("getOriginRepo() error: %v", err)
	}
	if got.FullName != "acme/rocket" {
		t.Fatalf("fullName = %q, want acme/rocket", got.FullName)
	}
}

func TestGetOriginRepoNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, originStatusError{Code: 5, Message: "not found"})
	}))
	defer server.Close()

	_, err := testOriginClient(server).getOriginRepo(context.Background(), "acme", "missing")
	if err == nil {
		t.Fatal("getOriginRepo() expected error, got nil")
	}
	apiErr, ok := err.(*originAPIError)
	if !ok {
		t.Fatalf("error type = %T, want *originAPIError", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", apiErr.StatusCode)
	}
	if !strings.Contains(err.Error(), "not found or not visible") {
		t.Errorf("error = %q, want not-found explanation", err.Error())
	}
}

func TestGetOriginRepoHTTPErrorWithoutJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := testOriginClient(server).getOriginRepo(context.Background(), "acme", "rocket")
	if err == nil {
		t.Fatal("getOriginRepo() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("error = %q, want HTTP 502", err.Error())
	}
}

func TestOriginRepoDataSourceReadByName(t *testing.T) {
	allowMerge := true
	allowSquash := false
	deleteBranch := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/Acme/Rocket" {
			t.Errorf("path = %s, want configured casing in the request path", r.URL.Path)
		}
		repo := sampleOriginRepo()
		repo.AllowMergeCommit = &allowMerge
		repo.AllowSquashMerge = &allowSquash
		repo.DeleteBranchOnMerge = &deleteBranch
		repo.PushedAt = ""
		repo.Mirror = nil
		writeJSON(t, w, http.StatusOK, repo)
	}))
	defer server.Close()

	state := readOriginRepoDataSource(t, testOriginClient(server), originRepoDataSourceModel{
		Owner: types.StringValue("Acme"),
		Name:  types.StringValue("Rocket"),
	})

	if state.ID.ValueString() != "repo_01rocket" {
		t.Errorf("id = %q", state.ID.ValueString())
	}
	if state.Owner.ValueString() != "Acme" || state.Name.ValueString() != "Rocket" {
		t.Errorf("owner/name = %s/%s, want configured casing preserved", state.Owner.ValueString(), state.Name.ValueString())
	}
	if state.FullName.ValueString() != "acme/rocket" {
		t.Errorf("full_name = %q", state.FullName.ValueString())
	}
	if state.OwnerID.ValueString() != "ns_01acme" || state.OwnerType.ValueString() != "team" {
		t.Errorf("owner id/type = %s/%s", state.OwnerID.ValueString(), state.OwnerType.ValueString())
	}
	if state.DefaultBranch.ValueString() != "main" || state.Visibility.ValueString() != "private" {
		t.Errorf("branch/visibility = %s/%s", state.DefaultBranch.ValueString(), state.Visibility.ValueString())
	}
	if state.CloneURL.ValueString() != "https://origin.cursor.com/acme/rocket.git" {
		t.Errorf("clone_url = %q", state.CloneURL.ValueString())
	}
	if !state.PushedAt.IsNull() {
		t.Errorf("pushed_at = %#v, want null", state.PushedAt)
	}
	if state.Mirror != nil {
		t.Errorf("mirror = %#v, want null", state.Mirror)
	}
	if !state.AllowMergeCommit.ValueBool() || state.AllowSquashMerge.ValueBool() || !state.DeleteBranchOnMerge.ValueBool() {
		t.Errorf("merge settings = merge %v squash %v delete %v", state.AllowMergeCommit.ValueBool(), state.AllowSquashMerge.ValueBool(), state.DeleteBranchOnMerge.ValueBool())
	}
}

func TestOriginRepoDataSourceReadByID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/_/repo_01rocket" {
			t.Errorf("path = %s, want ID lookup", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, sampleOriginRepo())
	}))
	defer server.Close()

	state := readOriginRepoDataSource(t, testOriginClient(server), originRepoDataSourceModel{
		ID: types.StringValue("repo_01rocket"),
	})

	if state.Owner.ValueString() != "acme" || state.Name.ValueString() != "rocket" {
		t.Errorf("owner/name = %s/%s, want values from the API", state.Owner.ValueString(), state.Name.ValueString())
	}
	if state.Mirror == nil || state.Mirror.Source.ValueString() != "github" || state.Mirror.SourceID.ValueString() != "gh_123" || state.Mirror.Status.ValueString() != "inbound" {
		t.Fatalf("mirror = %#v, want github inbound mirror", state.Mirror)
	}
	if !state.AllowMergeCommit.IsNull() {
		t.Errorf("allow_merge_commit = %#v, want null when omitted", state.AllowMergeCommit)
	}
}

func TestOriginRepoDataSourceReadRejectsMismatchedName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, sampleOriginRepo())
	}))
	defer server.Close()

	ds := &originRepoDataSource{client: testOriginClient(server)}
	_, diags := readOriginRepo(t, ds, originRepoDataSourceModel{
		ID:    types.StringValue("repo_01rocket"),
		Owner: types.StringValue("acme"),
		Name:  types.StringValue("other"),
	})
	if !diags.HasError() {
		t.Fatal("expected mismatch error")
	}
	if !strings.Contains(diags[0].Detail(), `does not match requested name "other"`) {
		t.Errorf("diagnostic = %s", diags[0].Detail())
	}
}

func TestOriginRepoLookupValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  originRepoDataSourceModel
		wantErr string
	}{
		{
			name:    "missing lookup",
			config:  originRepoDataSourceModel{},
			wantErr: "set owner and name, or set id",
		},
		{
			name: "owner without name",
			config: originRepoDataSourceModel{
				Owner: types.StringValue("acme"),
			},
			wantErr: "set both owner and name",
		},
		{
			name: "reserved owner",
			config: originRepoDataSourceModel{
				Owner: types.StringValue("_"),
				Name:  types.StringValue("repo_01rocket"),
			},
			wantErr: "reserved",
		},
		{
			name: "blank strings",
			config: originRepoDataSourceModel{
				Owner: types.StringValue("  "),
				Name:  types.StringValue(""),
			},
			wantErr: "set owner and name, or set id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveOriginRepoLookup(normalizeOriginConfig(tt.config))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestNewAPIClientSetsOriginAuth(t *testing.T) {
	client, err := newAPIClient("https://api2.cursor.sh", "session-token", "1.2.3", adminKeys{})
	if err != nil {
		t.Fatalf("newAPIClient() error: %v", err)
	}
	if client.originBase != defaultOriginAPIBase {
		t.Errorf("originBase = %q, want %q", client.originBase, defaultOriginAPIBase)
	}
	if client.authHeader != "Bearer session-token" {
		t.Errorf("authHeader = %q", client.authHeader)
	}
	if client.userAgent != "terraform-provider-cursor/1.2.3" {
		t.Errorf("userAgent = %q", client.userAgent)
	}
	if client.httpClient == nil {
		t.Fatal("httpClient is nil")
	}
}

func sampleOriginRepo() originRepo {
	return originRepo{
		ID:            "repo_01rocket",
		Name:          "rocket",
		FullName:      "acme/rocket",
		Owner:         &originOwner{Slug: "acme", ID: "ns_01acme", Type: "team"},
		DefaultBranch: "main",
		CreatedAt:     "2026-08-01T09:30:00Z",
		UpdatedAt:     "2026-08-02T14:45:00Z",
		PushedAt:      "2026-08-02T14:45:00Z",
		CloneURL:      "https://origin.cursor.com/acme/rocket.git",
		Visibility:    "private",
		Mirror: &originMirror{
			Source:   "github",
			SourceID: "gh_123",
			Status:   "inbound",
		},
	}
}

func testOriginClient(server *httptest.Server) *apiClient {
	return &apiClient{
		httpClient: server.Client(),
		authHeader: "Bearer session-token",
		userAgent:  "terraform-provider-cursor/test",
		originBase: server.URL,
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func readOriginRepoDataSource(t *testing.T, client *apiClient, config originRepoDataSourceModel) originRepoDataSourceModel {
	t.Helper()
	ds := &originRepoDataSource{client: client}
	state, diags := readOriginRepo(t, ds, config)
	if diags.HasError() {
		t.Fatalf("read diagnostics: %s", diags)
	}
	return state
}

func readOriginRepo(t *testing.T, ds *originRepoDataSource, config originRepoDataSourceModel) (originRepoDataSourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	schemaResp := &datasource.SchemaResponse{}
	ds.Schema(ctx, datasource.SchemaRequest{}, schemaResp)

	cfg := tfsdk.Config{
		Schema: schemaResp.Schema,
		Raw:    originConfigValue(t, ctx, schemaResp.Schema, normalizeOriginConfig(config)),
	}

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	ds.Read(ctx, datasource.ReadRequest{Config: cfg}, resp)

	var state originRepoDataSourceModel
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Get(ctx, &state)...)
	}
	return state, resp.Diagnostics
}

func originConfigValue(t *testing.T, ctx context.Context, schema datasourceschema.Schema, config originRepoDataSourceModel) tftypes.Value {
	t.Helper()

	tfType := schema.Type().TerraformType(ctx)
	objType, ok := tfType.(tftypes.Object)
	if !ok {
		t.Fatalf("schema type = %T, want tftypes.Object", tfType)
	}

	values := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, attrType := range objType.AttributeTypes {
		values[name] = tftypes.NewValue(attrType, nil)
	}
	setString := func(name string, value types.String) {
		if value.IsNull() || value.IsUnknown() {
			return
		}
		values[name] = tftypes.NewValue(objType.AttributeTypes[name], value.ValueString())
	}
	setString("id", config.ID)
	setString("owner", config.Owner)
	setString("name", config.Name)
	return tftypes.NewValue(tfType, values)
}

func normalizeOriginConfig(config originRepoDataSourceModel) originRepoDataSourceModel {
	if config.ID.IsNull() || config.ID.IsUnknown() {
		config.ID = types.StringNull()
	}
	if config.Owner.IsNull() || config.Owner.IsUnknown() {
		config.Owner = types.StringNull()
	}
	if config.Name.IsNull() || config.Name.IsUnknown() {
		config.Name = types.StringNull()
	}
	config.FullName = types.StringNull()
	config.OwnerID = types.StringNull()
	config.OwnerType = types.StringNull()
	config.DefaultBranch = types.StringNull()
	config.Visibility = types.StringNull()
	config.CloneURL = types.StringNull()
	config.CreatedAt = types.StringNull()
	config.UpdatedAt = types.StringNull()
	config.PushedAt = types.StringNull()
	config.AllowMergeCommit = types.BoolNull()
	config.AllowSquashMerge = types.BoolNull()
	config.DeleteBranchOnMerge = types.BoolNull()
	config.Mirror = nil
	return config
}
