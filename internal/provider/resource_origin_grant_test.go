package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestOriginRepoGrantReadRemovesMissingGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, originGrantList{Grants: sampleUserGrants(2)})
	}))
	defer server.Close()

	res := &originRepoGrantResource{client: testOriginClient(server)}
	state := repoGrantState(t, res, sampleRepoGrantModel())
	resp := &resource.ReadResponse{State: state}
	res.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Fatal("expected missing grant to be removed from state")
	}
}

func TestOriginRepoGrantReadRefreshesPermission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/rocket/grants" {
			t.Errorf("path = %s", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, originGrantList{Grants: []originGrant{
			{Group: &originGrantGroup{ID: "grp_01"}, Permission: originPermissionRead},
			{User: &originGrantUser{ID: "user_01"}, Permission: originPermissionCustom},
		}})
	}))
	defer server.Close()

	res := &originRepoGrantResource{client: testOriginClient(server)}
	model := sampleRepoGrantModel()
	model.ID = types.StringNull()
	model.UserEmail = types.StringValue("alice@acme.com")
	state := repoGrantState(t, res, model)
	resp := &resource.ReadResponse{State: state}
	res.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	var got originRepoGrantModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatal(diags)
	}
	if got.Permission.ValueString() != originPermissionCustom {
		t.Fatalf("permission = %q, want custom from API", got.Permission.ValueString())
	}
	if got.ID.ValueString() != "acme/rocket:user:user_01" || got.UserEmail.ValueString() != "alice@acme.com" {
		t.Fatalf("id/email = %q/%q", got.ID.ValueString(), got.UserEmail.ValueString())
	}
	if principalField(got.User, "id").ValueString() != "user_01" || !got.Group.IsNull() || !got.TeamGroup.IsNull() {
		t.Fatalf("principal = %#v", got)
	}
}

func TestOriginRepoGrantReadListErrorKeepsState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, originStatusError{Code: 5, Message: "missing"})
	}))
	defer server.Close()

	res := &originRepoGrantResource{client: testOriginClient(server)}
	state := repoGrantState(t, res, sampleRepoGrantModel())
	resp := &resource.ReadResponse{State: state}
	res.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected read error when the repository cannot be listed")
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("read removed state on list failure")
	}
}

func TestOriginRepoGrantCreateByID(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	res := &originRepoGrantResource{client: mock.client("key_team", "key_org")}
	plan := sampleRepoGrantModel()
	plan.ID = types.StringUnknown()
	plan.Permission = types.StringValue(originPermissionAdmin)
	got := createRepoGrant(t, res, plan)
	if body := mock.lastBody("POST /repos/acme/rocket/grants"); body != `{"user":{"id":"user_01"},"permission":"admin"}` {
		t.Fatalf("create body = %s", body)
	}
	if got.ID.ValueString() != "acme/rocket:user:user_01" || got.Permission.ValueString() != originPermissionAdmin {
		t.Fatalf("state = %#v", got)
	}
	if !got.UserEmail.IsNull() || !got.Group.IsNull() {
		t.Fatalf("unused principal attributes must be null: %#v", got)
	}
}

func TestOriginRepoGrantCreateResolvesUserEmailAtApply(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	res := &originRepoGrantResource{client: mock.client("key_team", "key_org")}
	plan := sampleRepoGrantModel()
	plan.ID = types.StringUnknown()
	plan.UserEmail = types.StringValue("Alice@Acme.com")
	plan.User = types.ObjectUnknown(originGrantUserAttrTypes)
	got := createRepoGrant(t, res, plan)
	if body := mock.lastBody("POST /repos/acme/rocket/grants"); body != `{"user":{"id":"user_alice"},"permission":"write"}` {
		t.Fatalf("create body = %s", body)
	}
	if got.ID.ValueString() != "acme/rocket:user:user_alice" || principalField(got.User, "id").ValueString() != "user_alice" {
		t.Fatalf("state = %#v", got)
	}
	if got.UserEmail.ValueString() != "Alice@Acme.com" {
		t.Fatalf("user_email = %q, want configured casing kept", got.UserEmail.ValueString())
	}
}

func TestOriginOwnerGrantCreateResolvesGroupName(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originOwnerGrantResource{client: mock.client("key_team", "key_org")}
	plan := originOwnerGrantModel{
		ID:         types.StringUnknown(),
		Owner:      types.StringValue("acme"),
		Permission: types.StringValue(originPermissionContributor),
		UserEmail:  types.StringNull(),
		GroupName:  types.StringValue("Engineering"),
		User:       types.ObjectNull(originGrantUserAttrTypes),
		Group:      types.ObjectUnknown(originGrantGroupAttrTypes),
		TeamGroup:  types.ObjectNull(originGrantTeamGroupAttrTypes),
	}
	planValue := tfsdk.Plan{Schema: ownerGrantSchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: planValue.Schema}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create diagnostics: %v", resp.Diagnostics)
	}
	if body := mock.lastBody("POST /owners/acme/grants"); body != `{"group":{"id":"grp_eng"},"permission":"PERMISSION_CONTRIBUTOR"}` {
		t.Fatalf("create body = %s", body)
	}
	var got originOwnerGrantModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if got.ID.ValueString() != "acme:group:grp_eng" || got.Permission.ValueString() != originPermissionContributor || got.GroupName.ValueString() != "Engineering" {
		t.Fatalf("state = %#v", got)
	}
}

func TestOriginRepoGrantModifyPlanResolvesEmailAndFlagsReplacement(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originRepoGrantResource{client: mock.client("key_team", "key_org")}
	sch := repoGrantSchema(t, res)

	plan := sampleRepoGrantModel()
	plan.ID = types.StringUnknown()
	plan.UserEmail = types.StringValue("alice@acme.com")
	plan.User = types.ObjectUnknown(originGrantUserAttrTypes)
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: emptyState(ctx, sch)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("modify plan diagnostics: %v", resp.Diagnostics)
	}
	var planned originRepoGrantModel
	if diags := resp.Plan.Get(ctx, &planned); diags.HasError() {
		t.Fatal(diags)
	}
	if principalField(planned.User, "id").ValueString() != "user_alice" || planned.ID.ValueString() != "acme/rocket:user:user_alice" {
		t.Fatalf("planned = %#v", planned)
	}
	if len(resp.RequiresReplace) != 0 {
		t.Fatalf("create must not require replace: %v", resp.RequiresReplace)
	}

	prior := planned
	prior.User = principalObject(originGrantUserAttrTypes, "id", "user_alice_old")
	prior.ID = types.StringValue("acme/rocket:user:user_alice_old")
	state := repoGrantState(t, res, prior)
	resp = &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("modify plan diagnostics: %v", resp.Diagnostics)
	}
	if len(resp.RequiresReplace) != 1 || !resp.RequiresReplace[0].Equal(path.Root("user")) {
		t.Fatalf("RequiresReplace = %v, want user", resp.RequiresReplace)
	}
	if diags := resp.Plan.Get(ctx, &planned); diags.HasError() {
		t.Fatal(diags)
	}
	if principalField(planned.User, "id").ValueString() != "user_alice" {
		t.Fatalf("planned user = %#v", planned.User)
	}

	prior.User = principalObject(originGrantUserAttrTypes, "id", "user_alice")
	state = repoGrantState(t, res, prior)
	resp = &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: state}, resp)
	if resp.Diagnostics.HasError() || len(resp.RequiresReplace) != 0 {
		t.Fatalf("unchanged resolution must not replace: %v %v", resp.Diagnostics, resp.RequiresReplace)
	}

	// No-change plan: UseStateForUnknown already copied the stale ID into the plan, so the email must still be re-resolved.
	stale := prior
	stale.User = principalObject(originGrantUserAttrTypes, "id", "user_alice_old")
	stale.ID = types.StringValue("acme/rocket:user:user_alice_old")
	stalePlan := tfsdk.Plan{Schema: sch}
	if diags := stalePlan.Set(ctx, &stale); diags.HasError() {
		t.Fatal(diags)
	}
	resp = &resource.ModifyPlanResponse{Plan: stalePlan}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: stalePlan, State: repoGrantState(t, res, stale)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("modify plan diagnostics: %v", resp.Diagnostics)
	}
	if len(resp.RequiresReplace) != 1 || !resp.RequiresReplace[0].Equal(path.Root("user")) {
		t.Fatalf("stale planned ID must be re-resolved and replaced, got %v", resp.RequiresReplace)
	}
	if diags := resp.Plan.Get(ctx, &planned); diags.HasError() {
		t.Fatal(diags)
	}
	if principalField(planned.User, "id").ValueString() != "user_alice" || planned.ID.ValueString() != "acme/rocket:user:user_alice" {
		t.Fatalf("planned = %#v", planned)
	}
}

func TestOriginRepoGrantCreateReusesPlannedResolution(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	res := &originRepoGrantResource{client: mock.client("key_team", "key_org")}
	plan := sampleRepoGrantModel()
	plan.ID = types.StringValue("acme/rocket:user:user_planned")
	plan.UserEmail = types.StringValue("alice@acme.com")
	plan.User = principalObject(originGrantUserAttrTypes, "id", "user_planned")
	got := createRepoGrant(t, res, plan)
	if body := mock.lastBody("POST /repos/acme/rocket/grants"); body != `{"user":{"id":"user_planned"},"permission":"write"}` {
		t.Fatalf("apply must use the planned ID, got %s", body)
	}
	if got.ID.ValueString() != "acme/rocket:user:user_planned" {
		t.Fatalf("id = %q", got.ID.ValueString())
	}
	if mock.count("GET /teams/members") != 0 {
		t.Fatal("apply must not re-resolve a planned principal")
	}
}

func TestOriginRepoGrantModifyPlanNormalizesComputedObjects(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originRepoGrantResource{client: mock.client("", "")}
	sch := repoGrantSchema(t, res)
	plan := sampleRepoGrantModel()
	plan.ID = types.StringUnknown()
	plan.User = types.ObjectUnknown(originGrantUserAttrTypes)
	plan.Group = principalObject(originGrantGroupAttrTypes, "id", "grp_01")
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: emptyState(ctx, sch)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("modify plan diagnostics: %v", resp.Diagnostics)
	}
	var planned originRepoGrantModel
	if diags := resp.Plan.Get(ctx, &planned); diags.HasError() {
		t.Fatal(diags)
	}
	if !planned.User.IsNull() || principalField(planned.Group, "id").ValueString() != "grp_01" || planned.ID.ValueString() != "acme/rocket:group:grp_01" {
		t.Fatalf("planned = %#v", planned)
	}
	if mock.count("GET /teams/members")+mock.count("GET /organizations/groups") != 0 {
		t.Fatal("ID principals must not call the Admin APIs")
	}
}

func TestOriginRepoGrantModifyPlanDefersUnknownEmailAndReportsMissingKey(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originRepoGrantResource{client: mock.client("", "")}
	sch := repoGrantSchema(t, res)
	plan := sampleRepoGrantModel()
	plan.ID = types.StringUnknown()
	plan.UserEmail = types.StringUnknown()
	plan.User = types.ObjectUnknown(originGrantUserAttrTypes)
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: emptyState(ctx, sch)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unknown email must defer: %v", resp.Diagnostics)
	}
	var planned originRepoGrantModel
	if diags := resp.Plan.Get(ctx, &planned); diags.HasError() {
		t.Fatal(diags)
	}
	if !planned.User.IsUnknown() || !planned.ID.IsUnknown() {
		t.Fatalf("planned = %#v, want unresolved", planned)
	}

	plan.UserEmail = types.StringValue("alice@acme.com")
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp = &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: emptyState(ctx, sch)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "team_api_key is required") {
		t.Fatalf("diagnostics = %v, want missing team_api_key", resp.Diagnostics)
	}
}

// Replace whose new repo is only known at apply. The prior composite id UseStateForUnknown copied into the plan
// cannot match the id written on apply, so ModifyPlan must leave it unknown.
func TestOriginRepoGrantModifyPlanUnknownRepoLeavesIDUnknown(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originRepoGrantResource{client: mock.client("", "")}
	sch := repoGrantSchema(t, res)

	prior := sampleRepoGrantModel()
	plan := prior
	plan.Repo = types.StringUnknown()
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: repoGrantState(t, res, prior)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("modify plan diagnostics: %v", resp.Diagnostics)
	}
	var planned originRepoGrantModel
	if diags := resp.Plan.Get(ctx, &planned); diags.HasError() {
		t.Fatal(diags)
	}
	if !planned.ID.IsUnknown() {
		t.Fatalf("planned id = %v, want unknown while repo is unknown", planned.ID)
	}
	if principalField(planned.User, "id").ValueString() != "user_01" || len(resp.RequiresReplace) != 0 {
		t.Fatalf("planned = %#v, replace = %v; the principal did not change", planned, resp.RequiresReplace)
	}

	planned.Repo = types.StringValue("booster")
	got := createRepoGrant(t, res, planned)
	if got.ID.ValueString() != "acme/booster:user:user_01" {
		t.Fatalf("id = %q", got.ID.ValueString())
	}
	if body := mock.lastBody("POST /repos/acme/booster/grants"); body != `{"user":{"id":"user_01"},"permission":"write"}` {
		t.Fatalf("create body = %s", body)
	}
}

func TestOriginOwnerGrantModifyPlanUnknownOwnerLeavesIDUnknown(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originOwnerGrantResource{client: mock.client("", "")}
	sch := ownerGrantSchema(t, res)

	prior := sampleOwnerTeamGrantModel(originTeamGroupAdmins, originPermissionAdmin)
	state := tfsdk.State{Schema: sch}
	if diags := state.Set(ctx, &prior); diags.HasError() {
		t.Fatal(diags)
	}
	modifyPlan := func(plan originOwnerGrantModel) originOwnerGrantModel {
		t.Helper()
		planValue := tfsdk.Plan{Schema: sch}
		if diags := planValue.Set(ctx, &plan); diags.HasError() {
			t.Fatal(diags)
		}
		resp := &resource.ModifyPlanResponse{Plan: planValue}
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: state}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("modify plan diagnostics: %v", resp.Diagnostics)
		}
		var planned originOwnerGrantModel
		if diags := resp.Plan.Get(ctx, &planned); diags.HasError() {
			t.Fatal(diags)
		}
		return planned
	}

	plan := prior
	plan.Owner = types.StringUnknown()
	planned := modifyPlan(plan)
	if !planned.ID.IsUnknown() {
		t.Fatalf("planned id = %v, want unknown while owner is unknown", planned.ID)
	}
	if principalField(planned.TeamGroup, "kind").ValueString() != originTeamGroupAdmins {
		t.Fatalf("planned = %#v", planned)
	}

	// With every input known the id is computed again and matches the prior value.
	planned = modifyPlan(prior)
	if planned.ID.ValueString() != "acme:team_group:admins" {
		t.Fatalf("planned id = %v, want the composite of owner and principal", planned.ID)
	}
}

func TestOriginOwnerGrantReadMapsCustomPermission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/owners/acme/grants" {
			t.Errorf("path = %s", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, originGrantList{Grants: []originGrant{
			{TeamGroup: &originGrantTeamGroup{Kind: originTeamGroupMembers}, Permission: "PERMISSION_CUSTOM"},
		}})
	}))
	defer server.Close()

	ctx := context.Background()
	res := &originOwnerGrantResource{client: testOriginClient(server)}
	model := sampleOwnerTeamGrantModel(originTeamGroupMembers, originPermissionRead)
	state := tfsdk.State{Schema: ownerGrantSchema(t, res)}
	if diags := state.Set(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.ReadResponse{State: state}
	res.Read(ctx, resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	var got originOwnerGrantModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if got.Permission.ValueString() != originPermissionCustom {
		t.Fatalf("permission = %q, want custom", got.Permission.ValueString())
	}
}

func TestOriginRepoGrantDeleteNotFoundIsSuccess(t *testing.T) {
	var deleteBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		deleteBody = strings.TrimSpace(string(raw))
		http.NotFound(w, r)
	}))
	defer server.Close()

	res := &originRepoGrantResource{client: testOriginClient(server)}
	state := repoGrantState(t, res, sampleRepoGrantModel())
	resp := &resource.DeleteResponse{}
	res.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete diagnostics: %v", resp.Diagnostics)
	}
	if deleteBody != `{"user":{"id":"user_01"}}` {
		t.Fatalf("delete body = %s", deleteBody)
	}
}

func TestOriginRepoGrantDeleteSurfacesPreconditionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusPreconditionFailed, originStatusError{Code: 9, Message: "cannot remove the last namespace admin"})
	}))
	defer server.Close()

	ctx := context.Background()
	res := &originOwnerGrantResource{client: testOriginClient(server)}
	model := sampleOwnerTeamGrantModel(originTeamGroupAdmins, originPermissionAdmin)
	state := tfsdk.State{Schema: ownerGrantSchema(t, res)}
	if diags := state.Set(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: state}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "last namespace admin") {
		t.Fatalf("diagnostics = %v, want precondition error", resp.Diagnostics)
	}
}

func TestOriginGrantImportState(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	ctx := context.Background()
	repoRes := &originRepoGrantResource{client: mock.client("key_team", "key_org")}
	sch := repoGrantSchema(t, repoRes)

	importRepo := func(id string) (originRepoGrantModel, *resource.ImportStateResponse) {
		resp := &resource.ImportStateResponse{State: emptyState(ctx, sch)}
		repoRes.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
		var model originRepoGrantModel
		if !resp.Diagnostics.HasError() {
			if diags := resp.State.Get(ctx, &model); diags.HasError() {
				t.Fatal(diags)
			}
		}
		return model, resp
	}

	got, resp := importRepo("acme/rocket:team_group:admins")
	if resp.Diagnostics.HasError() {
		t.Fatalf("import diagnostics: %v", resp.Diagnostics)
	}
	if got.Owner.ValueString() != "acme" || got.Repo.ValueString() != "rocket" || got.ID.ValueString() != "acme/rocket:team_group:admins" {
		t.Fatalf("imported = %#v", got)
	}
	if principalField(got.TeamGroup, "kind").ValueString() != originTeamGroupAdmins || !got.User.IsNull() || !got.Group.IsNull() || !got.UserEmail.IsNull() {
		t.Fatalf("imported principal = %#v", got)
	}
	if !got.Permission.IsNull() {
		t.Fatalf("permission = %v, want null until refresh", got.Permission)
	}

	got, resp = importRepo("acme/rocket:user_email:alice@acme.com")
	if resp.Diagnostics.HasError() {
		t.Fatalf("import by email diagnostics: %v", resp.Diagnostics)
	}
	if got.UserEmail.ValueString() != "alice@acme.com" || principalField(got.User, "id").ValueString() != "user_alice" || got.ID.ValueString() != "acme/rocket:user:user_alice" {
		t.Fatalf("imported by email = %#v", got)
	}

	got, resp = importRepo("acme/rocket:group_name:Engineering")
	if resp.Diagnostics.HasError() {
		t.Fatalf("import by group name diagnostics: %v", resp.Diagnostics)
	}
	if got.GroupName.ValueString() != "Engineering" || principalField(got.Group, "id").ValueString() != "grp_eng" || got.ID.ValueString() != "acme/rocket:group:grp_eng" {
		t.Fatalf("imported by group name = %#v", got)
	}

	if _, resp = importRepo("acme/rocket:user_email:nobody@acme.com"); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "no team member") {
		t.Fatalf("diagnostics = %v, want unresolved email", resp.Diagnostics)
	}
	if _, resp = importRepo("acme:user:user_01"); !resp.Diagnostics.HasError() {
		t.Fatal("expected repo grant import to reject an owner-only resource")
	}

	ownerRes := &originOwnerGrantResource{client: mock.client("key_team", "key_org")}
	ownerResp := &resource.ImportStateResponse{State: emptyState(ctx, ownerGrantSchema(t, ownerRes))}
	ownerRes.ImportState(ctx, resource.ImportStateRequest{ID: "acme:group:grp_01"}, ownerResp)
	if ownerResp.Diagnostics.HasError() {
		t.Fatalf("import diagnostics: %v", ownerResp.Diagnostics)
	}
	var ownerGrant originOwnerGrantModel
	if diags := ownerResp.State.Get(ctx, &ownerGrant); diags.HasError() {
		t.Fatal(diags)
	}
	if ownerGrant.Owner.ValueString() != "acme" || principalField(ownerGrant.Group, "id").ValueString() != "grp_01" {
		t.Fatalf("imported = %#v", ownerGrant)
	}

	ownerResp = &resource.ImportStateResponse{State: emptyState(ctx, ownerGrantSchema(t, ownerRes))}
	ownerRes.ImportState(ctx, resource.ImportStateRequest{ID: "acme/rocket:group:grp_01"}, ownerResp)
	if !ownerResp.Diagnostics.HasError() {
		t.Fatal("expected owner grant import to reject a repository resource")
	}
}

func TestValidateOriginGrants(t *testing.T) {
	valid := sampleRepoGrantModel()
	if err := validateOriginRepoGrant(valid, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectErr := func(name string, model originRepoGrantModel, config bool, want string) {
		t.Helper()
		if err := validateOriginRepoGrant(model, config); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: error = %v, want %q", name, err, want)
		}
	}

	none := valid
	none.User = types.ObjectNull(originGrantUserAttrTypes)
	expectErr("none", none, true, "exactly one")

	two := valid
	two.Group = principalObject(originGrantGroupAttrTypes, "id", "grp_01")
	expectErr("two", two, true, "exactly one")

	emailAndUser := valid
	emailAndUser.UserEmail = types.StringValue("alice@acme.com")
	expectErr("email and user", emailAndUser, true, "exactly one")
	if err := validateOriginRepoGrant(emailAndUser, false); err != nil {
		t.Fatalf("plan may carry user_email with the resolved user: %v", err)
	}

	unknownObject := valid
	unknownObject.User = types.ObjectUnknown(originGrantUserAttrTypes)
	if err := validateOriginRepoGrant(unknownObject, true); err != nil {
		t.Fatalf("unknown user object in config should validate: %v", err)
	}

	unknownID := valid
	unknownID.User = types.ObjectValueMust(originGrantUserAttrTypes, map[string]attr.Value{"id": types.StringUnknown()})
	if err := validateOriginRepoGrant(unknownID, true); err != nil {
		t.Fatalf("unknown user id should validate: %v", err)
	}

	email := valid
	email.User = types.ObjectNull(originGrantUserAttrTypes)
	email.UserEmail = types.StringValue("alice@acme.com")
	if err := validateOriginRepoGrant(email, true); err != nil {
		t.Fatalf("user_email alone should validate: %v", err)
	}
	email.UserEmail = types.StringValue("alice")
	expectErr("email shape", email, true, "not an email address")

	custom := valid
	custom.Permission = types.StringValue(originPermissionCustom)
	expectErr("custom", custom, true, "read-only")

	contributor := valid
	contributor.Permission = types.StringValue(originPermissionContributor)
	expectErr("contributor on repo", contributor, true, "permission must be one of read, write, admin")

	wireEnum := originOwnerGrantModel{
		Owner:      types.StringValue("acme"),
		Permission: types.StringValue("PERMISSION_WRITE"),
		UserEmail:  types.StringNull(),
		GroupName:  types.StringNull(),
		User:       valid.User,
		Group:      types.ObjectNull(originGrantGroupAttrTypes),
		TeamGroup:  types.ObjectNull(originGrantTeamGroupAttrTypes),
	}
	if err := validateOriginOwnerGrant(wireEnum, true); err == nil || !strings.Contains(err.Error(), "read, contributor, write, admin") {
		t.Fatalf("error = %v, want owner permission enum", err)
	}
	wireEnum.Permission = types.StringValue(originPermissionContributor)
	if err := validateOriginOwnerGrant(wireEnum, true); err != nil {
		t.Fatalf("contributor on owner grant: %v", err)
	}

	kind := valid
	kind.User = types.ObjectNull(originGrantUserAttrTypes)
	kind.TeamGroup = principalObject(originGrantTeamGroupAttrTypes, "kind", "everyone")
	expectErr("team kind", kind, true, "team_group.kind must be one of members, admins")

	wildcard := valid
	wildcard.User = principalObject(originGrantUserAttrTypes, "id", "~ALL")
	expectErr("wildcard", wildcard, true, "not a principal ID")

	badUserPrefix := valid
	badUserPrefix.User = principalObject(originGrantUserAttrTypes, "id", "act_01")
	expectErr("user prefix", badUserPrefix, true, "must start with user_")

	internalGroup := valid
	internalGroup.User = types.ObjectNull(originGrantUserAttrTypes)
	internalGroup.Group = principalObject(originGrantGroupAttrTypes, "id", "g_123")
	expectErr("g_ group", internalGroup, true, "g_ is the Organization Admin internal id")

	slash := valid
	slash.Repo = types.StringValue("rocket/nested")
	expectErr("slash", slash, true, "repo must not contain")

	padded := valid
	padded.Owner = types.StringValue(" acme")
	expectErr("padded", padded, true, "spaces")
}

func TestOriginGrantValidateConfigHandlesUnknownPrincipalObject(t *testing.T) {
	ctx := context.Background()
	res := &originRepoGrantResource{}
	sch := repoGrantSchema(t, res)
	objType := sch.Type().TerraformType(ctx).(tftypes.Object)
	values := map[string]tftypes.Value{
		"id":         tftypes.NewValue(tftypes.String, nil),
		"owner":      tftypes.NewValue(tftypes.String, "acme"),
		"repo":       tftypes.NewValue(tftypes.String, "rocket"),
		"permission": tftypes.NewValue(tftypes.String, originPermissionRead),
		"user_email": tftypes.NewValue(tftypes.String, nil),
		"group_name": tftypes.NewValue(tftypes.String, nil),
		"user":       tftypes.NewValue(objType.AttributeTypes["user"], tftypes.UnknownValue),
		"group":      tftypes.NewValue(objType.AttributeTypes["group"], nil),
		"team_group": tftypes.NewValue(objType.AttributeTypes["team_group"], nil),
	}
	config := tfsdk.Config{Schema: sch, Raw: tftypes.NewValue(objType, values)}
	resp := &resource.ValidateConfigResponse{}
	res.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: config}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unknown principal object should validate: %v", resp.Diagnostics)
	}

	values["user"] = tftypes.NewValue(objType.AttributeTypes["user"], nil)
	config = tfsdk.Config{Schema: sch, Raw: tftypes.NewValue(objType, values)}
	resp = &resource.ValidateConfigResponse{}
	res.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: config}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "exactly one") {
		t.Fatalf("diagnostics = %v, want missing principal error", resp.Diagnostics)
	}
}

func TestOriginGrantSchemaPlanModifiers(t *testing.T) {
	ctx := context.Background()
	for name, sch := range map[string]schema.Schema{
		"repo":  repoGrantSchema(t, &originRepoGrantResource{}),
		"owner": ownerGrantSchema(t, &originOwnerGrantResource{}),
	} {
		if diags := sch.ValidateImplementation(ctx); diags.HasError() {
			t.Fatalf("%s schema implementation: %v", name, diags)
		}
		for _, attr := range []string{"user", "group"} {
			nested := sch.Attributes[attr].(schema.SingleNestedAttribute)
			if !nested.Optional || !nested.Computed || len(nested.PlanModifiers) != 2 {
				t.Fatalf("%s.%s must be Optional+Computed with RequiresReplaceIfConfigured and UseStateForUnknown", name, attr)
			}
		}
		if len(sch.Attributes["team_group"].(schema.SingleNestedAttribute).PlanModifiers) != 1 {
			t.Fatalf("%s.team_group must require replace", name)
		}
		for _, attr := range []string{"user_email", "group_name", "owner"} {
			if len(sch.Attributes[attr].(schema.StringAttribute).PlanModifiers) != 1 {
				t.Fatalf("%s.%s must require replace", name, attr)
			}
		}
		if len(sch.Attributes["permission"].(schema.StringAttribute).PlanModifiers) != 0 {
			t.Fatalf("%s.permission must update in place", name)
		}
		// ModifyPlan derives id from owner, repo, and the resolved principal; a copied prior value would go stale on replace.
		if len(sch.Attributes["id"].(schema.StringAttribute).PlanModifiers) != 0 {
			t.Fatalf("%s.id must not carry UseStateForUnknown", name)
		}
	}
}

type grantMock struct {
	*httptest.Server
	t    *testing.T
	mu   sync.Mutex
	hits map[string][]string
}

// newGrantMock serves the Origin grants endpoints plus the Team and Organization Admin lookups.
func newGrantMock(t *testing.T) *grantMock {
	m := &grantMock{t: t, hits: map[string][]string{}}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		route := r.Method + " " + r.URL.Path
		m.hits[route] = append(m.hits[route], strings.TrimSpace(string(raw)))
		m.mu.Unlock()
		switch {
		case r.URL.Path == "/teams/members":
			writeJSON(t, w, http.StatusOK, adminTeamMembersResponse{TeamMembers: []adminTeamMember{
				{ID: "1", PublicID: "user_alice", Email: "alice@acme.com"},
				{ID: "2", PublicID: "user_bob", Email: "bob@acme.com"},
			}})
		case r.URL.Path == "/organizations/groups":
			groups := []adminGroup{}
			if r.URL.Query().Get("name") == "Engineering" {
				groups = append(groups, adminGroup{ID: "g_1", PublicID: "grp_eng", Name: "Engineering"})
			}
			writeJSON(t, w, http.StatusOK, adminGroupsResponse{Groups: groups})
		case r.Method == http.MethodPost:
			var grant originGrant
			if err := json.Unmarshal(raw, &grant); err != nil {
				t.Errorf("decode grant: %v", err)
			}
			writeJSON(t, w, http.StatusOK, grant)
		case r.Method == http.MethodGet:
			writeJSON(t, w, http.StatusOK, originGrantList{})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	return m
}

func (m *grantMock) client(team, org string) *apiClient {
	return &apiClient{
		httpClient: m.Client(),
		authHeader: "Bearer session-token",
		userAgent:  "terraform-provider-cursor/test",
		originBase: m.URL,
		adminBase:  m.URL,
		teamAPIKey: team,
		orgAPIKey:  org,
	}
}

func (m *grantMock) lastBody(route string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	bodies := m.hits[route]
	if len(bodies) == 0 {
		m.t.Fatalf("no request recorded for %s", route)
	}
	return bodies[len(bodies)-1]
}

func (m *grantMock) count(route string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.hits[route])
}

func createRepoGrant(t *testing.T, res *originRepoGrantResource, plan originRepoGrantModel) originRepoGrantModel {
	t.Helper()
	ctx := context.Background()
	planValue := tfsdk.Plan{Schema: repoGrantSchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: planValue.Schema}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create diagnostics: %v", resp.Diagnostics)
	}
	var got originRepoGrantModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	return got
}

func sampleRepoGrantModel() originRepoGrantModel {
	return originRepoGrantModel{
		ID:         types.StringValue("acme/rocket:user:user_01"),
		Owner:      types.StringValue("acme"),
		Repo:       types.StringValue("rocket"),
		Permission: types.StringValue(originPermissionWrite),
		UserEmail:  types.StringNull(),
		GroupName:  types.StringNull(),
		User:       principalObject(originGrantUserAttrTypes, "id", "user_01"),
		Group:      types.ObjectNull(originGrantGroupAttrTypes),
		TeamGroup:  types.ObjectNull(originGrantTeamGroupAttrTypes),
	}
}

func sampleOwnerTeamGrantModel(kind, permission string) originOwnerGrantModel {
	return originOwnerGrantModel{
		ID:         types.StringValue("acme:team_group:" + kind),
		Owner:      types.StringValue("acme"),
		Permission: types.StringValue(permission),
		UserEmail:  types.StringNull(),
		GroupName:  types.StringNull(),
		User:       types.ObjectNull(originGrantUserAttrTypes),
		Group:      types.ObjectNull(originGrantGroupAttrTypes),
		TeamGroup:  principalObject(originGrantTeamGroupAttrTypes, "kind", kind),
	}
}

func repoGrantSchema(t *testing.T, res *originRepoGrantResource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	res.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func ownerGrantSchema(t *testing.T, res *originOwnerGrantResource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	res.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// Mirrors the null object the framework hands to ImportState.
func emptyState(ctx context.Context, sch schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
}

func repoGrantState(t *testing.T, res *originRepoGrantResource, model originRepoGrantModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: repoGrantSchema(t, res)}
	if diags := state.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return state
}
