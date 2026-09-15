package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestOriginRepoGrantsDataSourceRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/rocket/grants" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, originGrantList{Grants: []originGrant{
			{User: &originGrantUser{ID: "user_01"}, Permission: originPermissionAdmin},
			{Group: &originGrantGroup{ID: "grp_01"}, Permission: originPermissionWrite},
			{TeamGroup: &originGrantTeamGroup{Kind: originTeamGroupMembers}, Permission: originPermissionRead},
		}})
	}))
	defer server.Close()

	ctx := context.Background()
	ds := &originRepoGrantsDataSource{client: testOriginClient(server)}
	schemaResp := &datasource.SchemaResponse{}
	ds.Schema(ctx, datasource.SchemaRequest{}, schemaResp)
	objType := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objType, map[string]tftypes.Value{
		"owner":  tftypes.NewValue(tftypes.String, "acme"),
		"repo":   tftypes.NewValue(tftypes.String, "rocket"),
		"grants": tftypes.NewValue(objType.AttributeTypes["grants"], nil),
	})}

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	ds.Read(ctx, datasource.ReadRequest{Config: config}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	var state originRepoGrantsDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatal(diags)
	}
	if len(state.Grants) != 3 {
		t.Fatalf("grants = %#v", state.Grants)
	}
	if state.Grants[0].ID.ValueString() != "acme/rocket:user:user_01" || state.Grants[0].User == nil || state.Grants[0].Group != nil || state.Grants[0].TeamGroup != nil {
		t.Fatalf("user grant = %#v", state.Grants[0])
	}
	if state.Grants[1].ID.ValueString() != "acme/rocket:group:grp_01" || state.Grants[1].Permission.ValueString() != originPermissionWrite {
		t.Fatalf("group grant = %#v", state.Grants[1])
	}
	if state.Grants[2].ID.ValueString() != "acme/rocket:team_group:members" || state.Grants[2].TeamGroup == nil || state.Grants[2].TeamGroup.Kind.ValueString() != originTeamGroupMembers {
		t.Fatalf("team group grant = %#v", state.Grants[2])
	}
}

func TestOriginOwnerGrantsDataSourceReadMapsPermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/owners/acme/grants" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, originGrantList{Grants: []originGrant{
			{TeamGroup: &originGrantTeamGroup{Kind: originTeamGroupAdmins}, Permission: "PERMISSION_ADMIN"},
			{Group: &originGrantGroup{ID: "grp_02"}, Permission: "PERMISSION_CUSTOM"},
		}})
	}))
	defer server.Close()

	ctx := context.Background()
	ds := &originOwnerGrantsDataSource{client: testOriginClient(server)}
	schemaResp := &datasource.SchemaResponse{}
	ds.Schema(ctx, datasource.SchemaRequest{}, schemaResp)
	objType := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objType, map[string]tftypes.Value{
		"owner":  tftypes.NewValue(tftypes.String, "acme"),
		"grants": tftypes.NewValue(objType.AttributeTypes["grants"], nil),
	})}

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	ds.Read(ctx, datasource.ReadRequest{Config: config}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	var state originOwnerGrantsDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatal(diags)
	}
	if len(state.Grants) != 2 {
		t.Fatalf("grants = %#v", state.Grants)
	}
	if state.Grants[0].ID.ValueString() != "acme:team_group:admins" || state.Grants[0].Permission.ValueString() != originPermissionAdmin {
		t.Fatalf("admins grant = %#v", state.Grants[0])
	}
	if state.Grants[1].Permission.ValueString() != originPermissionCustom {
		t.Fatalf("custom grant permission = %q", state.Grants[1].Permission.ValueString())
	}
	if state.Owner.ValueString() != "acme" {
		t.Fatalf("owner = %q", state.Owner.ValueString())
	}
}

func TestOriginGrantsDataSourceRejectsBadSlug(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	ctx := context.Background()
	ds := &originRepoGrantsDataSource{client: testOriginClient(server)}
	schemaResp := &datasource.SchemaResponse{}
	ds.Schema(ctx, datasource.SchemaRequest{}, schemaResp)
	objType := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objType, map[string]tftypes.Value{
		"owner":  tftypes.NewValue(tftypes.String, "acme"),
		"repo":   tftypes.NewValue(tftypes.String, "rocket:bad"),
		"grants": tftypes.NewValue(objType.AttributeTypes["grants"], nil),
	})}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	ds.Read(ctx, datasource.ReadRequest{Config: config}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected slug validation error")
	}
}
