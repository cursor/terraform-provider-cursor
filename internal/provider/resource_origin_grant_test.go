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
	configured := plan
	configured.User = types.ObjectNull(originGrantUserAttrTypes)
	config := repoGrantConfig(t, sch, configured)
	resp := &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: config, Plan: planValue, State: emptyState(ctx, sch)}, resp)
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
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: config, Plan: planValue, State: state}, resp)
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
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: config, Plan: planValue, State: state}, resp)
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
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: config, Plan: stalePlan, State: repoGrantState(t, res, stale)}, resp)
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
	configured := plan
	configured.User = types.ObjectNull(originGrantUserAttrTypes)
	resp := &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: repoGrantConfig(t, sch, configured), Plan: planValue, State: emptyState(ctx, sch)}, resp)
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
	configured := plan
	configured.User = types.ObjectNull(originGrantUserAttrTypes)
	resp := &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: repoGrantConfig(t, sch, configured), Plan: planValue, State: emptyState(ctx, sch)}, resp)
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
	configured.UserEmail = plan.UserEmail
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp = &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: repoGrantConfig(t, sch, configured), Plan: planValue, State: emptyState(ctx, sch)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "team_api_key is required") {
		t.Fatalf("diagnostics = %v, want missing team_api_key", resp.Diagnostics)
	}
}

// Replace whose new user_email is only known at apply. UseStateForUnknown has already copied the prior user and id
// into the plan; ModifyPlan must mark them unknown, or Create would trust the stored ID and grant the previous user.
func TestOriginRepoGrantModifyPlanUnknownEmailDropsStalePrincipal(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originRepoGrantResource{client: mock.client("key_team", "key_org")}
	sch := repoGrantSchema(t, res)

	prior := sampleRepoGrantModel()
	prior.ID = types.StringValue("acme/rocket:user:user_alice")
	prior.UserEmail = types.StringValue("alice@acme.com")
	prior.User = principalObject(originGrantUserAttrTypes, "id", "user_alice")

	plan := prior
	plan.UserEmail = types.StringUnknown()
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	configured := plan
	configured.User = types.ObjectNull(originGrantUserAttrTypes)
	resp := &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: repoGrantConfig(t, sch, configured), Plan: planValue, State: repoGrantState(t, res, prior)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("modify plan diagnostics: %v", resp.Diagnostics)
	}
	var planned originRepoGrantModel
	if diags := resp.Plan.Get(ctx, &planned); diags.HasError() {
		t.Fatal(diags)
	}
	if !planned.User.IsUnknown() || !planned.ID.IsUnknown() {
		t.Fatalf("planned = %#v, want user and id unknown until user_email is known", planned)
	}
	if !planned.UserEmail.IsUnknown() || !planned.Group.IsNull() || !planned.TeamGroup.IsNull() {
		t.Fatalf("planned = %#v", planned)
	}
	if mock.count("GET /teams/members") != 0 {
		t.Fatal("an unknown email cannot be resolved at plan")
	}

	// Apply sees the email; the user is still unknown, so it is resolved instead of reusing the prior ID.
	planned.UserEmail = types.StringValue("bob@acme.com")
	got := createRepoGrant(t, res, planned)
	if body := mock.lastBody("POST /repos/acme/rocket/grants"); body != `{"user":{"id":"user_bob"},"permission":"write"}` {
		t.Fatalf("create body = %s, want the newly resolved user", body)
	}
	if got.ID.ValueString() != "acme/rocket:user:user_bob" || principalField(got.User, "id").ValueString() != "user_bob" {
		t.Fatalf("state = %#v", got)
	}
}

func TestOriginOwnerGrantModifyPlanUnknownGroupNameDropsStalePrincipal(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originOwnerGrantResource{client: mock.client("key_team", "key_org")}
	sch := ownerGrantSchema(t, res)

	prior := originOwnerGrantModel{
		ID:         types.StringValue("acme:group:grp_eng"),
		Owner:      types.StringValue("acme"),
		Permission: types.StringValue(originPermissionRead),
		UserEmail:  types.StringNull(),
		GroupName:  types.StringValue("Engineering"),
		User:       types.ObjectNull(originGrantUserAttrTypes),
		Group:      principalObject(originGrantGroupAttrTypes, "id", "grp_eng"),
		TeamGroup:  types.ObjectNull(originGrantTeamGroupAttrTypes),
	}
	state := tfsdk.State{Schema: sch}
	if diags := state.Set(ctx, &prior); diags.HasError() {
		t.Fatal(diags)
	}
	// The plan carries the prior group through UseStateForUnknown; the configuration never sets it.
	modifyPlan := func(plan originOwnerGrantModel) originOwnerGrantModel {
		t.Helper()
		planValue := tfsdk.Plan{Schema: sch}
		if diags := planValue.Set(ctx, &plan); diags.HasError() {
			t.Fatal(diags)
		}
		configured := plan
		configured.Group = types.ObjectNull(originGrantGroupAttrTypes)
		resp := &resource.ModifyPlanResponse{Plan: planValue}
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: ownerGrantConfig(t, sch, configured), Plan: planValue, State: state}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("modify plan diagnostics: %v", resp.Diagnostics)
		}
		var planned originOwnerGrantModel
		if diags := resp.Plan.Get(ctx, &planned); diags.HasError() {
			t.Fatal(diags)
		}
		return planned
	}

	// Same family: group_name becomes unknown while UseStateForUnknown kept the prior group and id.
	plan := prior
	plan.GroupName = types.StringUnknown()
	planned := modifyPlan(plan)
	if !planned.Group.IsUnknown() || !planned.ID.IsUnknown() || !planned.User.IsNull() {
		t.Fatalf("planned = %#v, want group and id unknown until group_name is known", planned)
	}

	// Family switch: user_email replaces group_name, so the prior group must not survive as a second principal.
	plan = prior
	plan.GroupName = types.StringNull()
	plan.UserEmail = types.StringUnknown()
	planned = modifyPlan(plan)
	if !planned.User.IsUnknown() || !planned.Group.IsNull() || !planned.ID.IsUnknown() {
		t.Fatalf("planned = %#v, want user unknown and the prior group dropped", planned)
	}
	if mock.count("GET /organizations/groups")+mock.count("GET /teams/members") != 0 {
		t.Fatal("unknown names cannot be resolved at plan")
	}
}

// Replace whose new principal is a known ID object. UseStateForUnknown has already copied the prior
// user or group into the plan; ModifyPlan must drop it, or Create would reject two principals.
func TestOriginRepoGrantModifyPlanKnownPrincipalSwitchDropsStaleObject(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originRepoGrantResource{client: mock.client("", "")}
	sch := repoGrantSchema(t, res)

	prior := sampleRepoGrantModel()
	state := repoGrantState(t, res, prior)
	// The configuration names only the new principal; the plan also carries the prior user through UseStateForUnknown.
	modifyPlan := func(plan originRepoGrantModel) originRepoGrantModel {
		t.Helper()
		planValue := tfsdk.Plan{Schema: sch}
		if diags := planValue.Set(ctx, &plan); diags.HasError() {
			t.Fatal(diags)
		}
		configured := plan
		configured.User = types.ObjectNull(originGrantUserAttrTypes)
		resp := &resource.ModifyPlanResponse{Plan: planValue}
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: repoGrantConfig(t, sch, configured), Plan: planValue, State: state}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("modify plan diagnostics: %v", resp.Diagnostics)
		}
		var planned originRepoGrantModel
		if diags := resp.Plan.Get(ctx, &planned); diags.HasError() {
			t.Fatal(diags)
		}
		return planned
	}

	// UseStateForUnknown has copied the prior user into the plan alongside the new principal.
	plan := prior
	plan.TeamGroup = principalObject(originGrantTeamGroupAttrTypes, "kind", originTeamGroupMembers)
	planned := modifyPlan(plan)
	if !planned.User.IsNull() || !planned.Group.IsNull() || principalField(planned.TeamGroup, "kind").ValueString() != originTeamGroupMembers {
		t.Fatalf("planned = %#v, want prior user dropped and team_group kept", planned)
	}
	if planned.ID.ValueString() != "acme/rocket:team_group:members" {
		t.Fatalf("id = %q", planned.ID.ValueString())
	}
	got := createRepoGrant(t, res, planned)
	if body := mock.lastBody("POST /repos/acme/rocket/grants"); body != `{"teamGroup":{"kind":"members"},"permission":"write"}` {
		t.Fatalf("create body = %s, want the new team_group grant", body)
	}
	if got.ID.ValueString() != "acme/rocket:team_group:members" {
		t.Fatalf("state id = %q", got.ID.ValueString())
	}

	plan = prior
	plan.Group = principalObject(originGrantGroupAttrTypes, "id", "grp_01")
	planned = modifyPlan(plan)
	if !planned.User.IsNull() || principalField(planned.Group, "id").ValueString() != "grp_01" || !planned.TeamGroup.IsNull() {
		t.Fatalf("planned = %#v, want prior user dropped and group kept", planned)
	}
	if planned.ID.ValueString() != "acme/rocket:group:grp_01" {
		t.Fatalf("id = %q", planned.ID.ValueString())
	}
	got = createRepoGrant(t, res, planned)
	if body := mock.lastBody("POST /repos/acme/rocket/grants"); body != `{"group":{"id":"grp_01"},"permission":"write"}` {
		t.Fatalf("create body = %s, want the new group grant", body)
	}
	if got.ID.ValueString() != "acme/rocket:group:grp_01" {
		t.Fatalf("state id = %q", got.ID.ValueString())
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
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: repoGrantConfig(t, sch, plan), Plan: planValue, State: repoGrantState(t, res, prior)}, resp)
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
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: ownerGrantConfig(t, sch, plan), Plan: planValue, State: state}, resp)
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

// Every move between principal families, on both grant resources. The framework plan hands ModifyPlan the previous
// computed user or group next to the newly configured principal (UseStateForUnknown), so without normalisation the
// plan names two principals: destroy would remove the old grant and the create half would then be rejected.
func TestOriginGrantModifyPlanFamilySwitchesKeepOnePrincipal(t *testing.T) {
	ctx := context.Background()
	for name, newHarness := range grantHarnesses() {
		for _, prior := range grantPrincipalCases() {
			for _, next := range grantPrincipalCases() {
				t.Run(name+"/"+prior.family+"_to_"+next.family, func(t *testing.T) {
					mock := newGrantMock(t)
					defer mock.Close()
					h := newHarness(mock.client("key_team", "key_org"))
					sch := h.schema(t)

					stored := prior.config.withKey(prior.key)
					state := tfsdk.State{Schema: sch, Raw: h.raw(t, stored, types.StringValue(originGrantID(h.resource(), prior.key)))}
					config := tfsdk.Config{Schema: sch, Raw: h.raw(t, next.config, types.StringNull())}
					proposed := proposedPrincipal(next.config, &stored)
					plan := tfsdk.Plan{Schema: sch, Raw: h.raw(t, proposed, types.StringUnknown())}

					idFamily := next.family == originGrantKindUser || next.family == originGrantKindGroup || next.family == originGrantKindTeamGroup
					if carried := prior.key.kind != originGrantKindTeamGroup && next.family != prior.key.kind; carried && idFamily {
						if _, err := proposed.family(false); err == nil {
							t.Fatal("test setup: the proposed plan should still carry the previous principal next to the new one")
						}
					}

					resp := &resource.ModifyPlanResponse{Plan: plan}
					h.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: config, Plan: plan, State: state}, resp)
					if resp.Diagnostics.HasError() {
						t.Fatalf("modify plan diagnostics: %v", resp.Diagnostics)
					}
					planned, id := h.principal(t, resp.Plan.Raw)
					assertSinglePrincipal(t, planned, next.key)
					if id.ValueString() != originGrantID(h.resource(), next.key) {
						t.Fatalf("planned id = %v, want %q", id, originGrantID(h.resource(), next.key))
					}
					if !planned.UserEmail.Equal(next.config.UserEmail) || !planned.GroupName.Equal(next.config.GroupName) {
						t.Fatalf("planned names = %v/%v, want the configured %v/%v", planned.UserEmail, planned.GroupName, next.config.UserEmail, next.config.GroupName)
					}
					replace := prior.key != next.key
					switch {
					case replace && (len(resp.RequiresReplace) != 1 || !resp.RequiresReplace[0].Equal(path.Root(next.key.kind))):
						t.Fatalf("RequiresReplace = %v, want %s", resp.RequiresReplace, next.key.kind)
					case !replace && len(resp.RequiresReplace) != 0:
						t.Fatalf("RequiresReplace = %v, want none for an unchanged principal", resp.RequiresReplace)
					}

					// Apply: a replace destroys the prior grant first, then the create half must succeed with one principal.
					var applied tftypes.Value
					if replace {
						deleteResp := &resource.DeleteResponse{}
						h.Delete(ctx, resource.DeleteRequest{State: state}, deleteResp)
						if deleteResp.Diagnostics.HasError() {
							t.Fatalf("delete diagnostics: %v", deleteResp.Diagnostics)
						}
						if body, want := mock.lastBody("DELETE "+h.route()), wireJSON(t, prior.key.wire()); body != want {
							t.Fatalf("delete body = %s, want %s", body, want)
						}
						createResp := &resource.CreateResponse{State: tfsdk.State{Schema: sch}}
						h.Create(ctx, resource.CreateRequest{Plan: resp.Plan}, createResp)
						if createResp.Diagnostics.HasError() {
							t.Fatalf("create after destroy failed, access to the prior grant is already gone: %v", createResp.Diagnostics)
						}
						applied = createResp.State.Raw
					} else {
						updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
						h.Update(ctx, resource.UpdateRequest{Config: config, Plan: resp.Plan, State: state}, updateResp)
						if updateResp.Diagnostics.HasError() {
							t.Fatalf("update diagnostics: %v", updateResp.Diagnostics)
						}
						applied = updateResp.State.Raw
					}
					grant := next.key.wire()
					grant.Permission = h.wirePermission()
					if body, want := mock.lastBody("POST "+h.route()), wireJSON(t, grant); body != want {
						t.Fatalf("upsert body = %s, want %s", body, want)
					}
					final, finalID := h.principal(t, applied)
					assertSinglePrincipal(t, final, next.key)
					if finalID.ValueString() != originGrantID(h.resource(), next.key) {
						t.Fatalf("applied id = %v", finalID)
					}
				})
			}
		}
	}
}

// A principal whose identifier comes from another resource is unknown at plan. ModifyPlan must keep the configured
// object as written, drop any carried previous principal, and leave the composite id unknown; the apply-time plan
// then resolves the known value, and the create posts exactly one principal.
func TestOriginGrantModifyPlanDefersUnknownPrincipalIDs(t *testing.T) {
	ctx := context.Background()
	unknownID := func(attrTypes map[string]attr.Type, field string) types.Object {
		return types.ObjectValueMust(attrTypes, map[string]attr.Value{field: types.StringUnknown()})
	}
	byFamily := map[string]grantPrincipalCase{}
	for _, c := range grantPrincipalCases() {
		byFamily[c.family] = c
	}
	cases := []struct {
		name   string
		config originGrantPrincipal
		known  originGrantPrincipal
		key    originGrantKey
		prior  string
	}{
		{"user id unknown on create", nullPrincipal().withUser(unknownID(originGrantUserAttrTypes, "id")), nullPrincipal().withUser(principalObject(originGrantUserAttrTypes, "id", "user_02")), originGrantKey{kind: originGrantKindUser, value: "user_02"}, ""},
		{"group id unknown on create", nullPrincipal().withGroup(unknownID(originGrantGroupAttrTypes, "id")), nullPrincipal().withGroup(principalObject(originGrantGroupAttrTypes, "id", "grp_02")), originGrantKey{kind: originGrantKindGroup, value: "grp_02"}, ""},
		{"team_group kind unknown on create", nullPrincipal().withTeamGroup(unknownID(originGrantTeamGroupAttrTypes, "kind")), nullPrincipal().withTeamGroup(principalObject(originGrantTeamGroupAttrTypes, "kind", originTeamGroupAdmins)), originGrantKey{kind: originGrantKindTeamGroup, value: originTeamGroupAdmins}, ""},
		{"user object unknown on create", nullPrincipal().withUser(types.ObjectUnknown(originGrantUserAttrTypes)), nullPrincipal().withUser(principalObject(originGrantUserAttrTypes, "id", "user_02")), originGrantKey{kind: originGrantKindUser, value: "user_02"}, ""},
		{"group object unknown on create", nullPrincipal().withGroup(types.ObjectUnknown(originGrantGroupAttrTypes)), nullPrincipal().withGroup(principalObject(originGrantGroupAttrTypes, "id", "grp_02")), originGrantKey{kind: originGrantKindGroup, value: "grp_02"}, ""},
		{"team_group object unknown on create", nullPrincipal().withTeamGroup(types.ObjectUnknown(originGrantTeamGroupAttrTypes)), nullPrincipal().withTeamGroup(principalObject(originGrantTeamGroupAttrTypes, "kind", originTeamGroupMembers)), originGrantKey{kind: originGrantKindTeamGroup, value: originTeamGroupMembers}, ""},
		{"user id unknown replacing a group_name grant", nullPrincipal().withUser(unknownID(originGrantUserAttrTypes, "id")), nullPrincipal().withUser(principalObject(originGrantUserAttrTypes, "id", "user_02")), originGrantKey{kind: originGrantKindUser, value: "user_02"}, principalFamilyGroupName},
		{"group id unknown replacing a user_email grant", nullPrincipal().withGroup(unknownID(originGrantGroupAttrTypes, "id")), nullPrincipal().withGroup(principalObject(originGrantGroupAttrTypes, "id", "grp_02")), originGrantKey{kind: originGrantKindGroup, value: "grp_02"}, principalFamilyUserEmail},
		{"team_group kind unknown replacing a user grant", nullPrincipal().withTeamGroup(unknownID(originGrantTeamGroupAttrTypes, "kind")), nullPrincipal().withTeamGroup(principalObject(originGrantTeamGroupAttrTypes, "kind", originTeamGroupAdmins)), originGrantKey{kind: originGrantKindTeamGroup, value: originTeamGroupAdmins}, originGrantKindUser},
		{"user id unknown replacing a user grant", nullPrincipal().withUser(unknownID(originGrantUserAttrTypes, "id")), nullPrincipal().withUser(principalObject(originGrantUserAttrTypes, "id", "user_02")), originGrantKey{kind: originGrantKindUser, value: "user_02"}, originGrantKindUser},
	}
	for name, newHarness := range grantHarnesses() {
		for _, c := range cases {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				mock := newGrantMock(t)
				defer mock.Close()
				h := newHarness(mock.client("", ""))
				sch := h.schema(t)

				state := emptyState(ctx, sch)
				var stored *originGrantPrincipal
				if c.prior != "" {
					prior := byFamily[c.prior]
					p := prior.config.withKey(prior.key)
					stored = &p
					state = tfsdk.State{Schema: sch, Raw: h.raw(t, p, types.StringValue(originGrantID(h.resource(), prior.key)))}
				}
				config := tfsdk.Config{Schema: sch, Raw: h.raw(t, c.config, types.StringNull())}
				plan := tfsdk.Plan{Schema: sch, Raw: h.raw(t, proposedPrincipal(c.config, stored), types.StringUnknown())}
				resp := &resource.ModifyPlanResponse{Plan: plan}
				h.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: config, Plan: plan, State: state}, resp)
				if resp.Diagnostics.HasError() {
					t.Fatalf("an unknown identifier must defer, got %v", resp.Diagnostics)
				}
				planned, id := h.principal(t, resp.Plan.Raw)
				if !id.IsUnknown() {
					t.Fatalf("planned id = %v, want unknown until the principal is known", id)
				}
				if !planned.User.Equal(c.config.User) || !planned.Group.Equal(c.config.Group) || !planned.TeamGroup.Equal(c.config.TeamGroup) {
					t.Fatalf("planned principal = %#v, want exactly the configured objects %#v", planned, c.config)
				}
				if mock.count("GET /teams/members")+mock.count("GET /organizations/groups") != 0 {
					t.Fatal("ID principals must not call the Admin APIs")
				}

				// Apply plans the create half again with the identifier known and no prior state.
				knownConfig := tfsdk.Config{Schema: sch, Raw: h.raw(t, c.known, types.StringNull())}
				knownPlan := tfsdk.Plan{Schema: sch, Raw: h.raw(t, proposedPrincipal(c.known, nil), types.StringUnknown())}
				applyResp := &resource.ModifyPlanResponse{Plan: knownPlan}
				h.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: knownConfig, Plan: knownPlan, State: emptyState(ctx, sch)}, applyResp)
				if applyResp.Diagnostics.HasError() {
					t.Fatalf("apply-time plan diagnostics: %v", applyResp.Diagnostics)
				}
				final, finalID := h.principal(t, applyResp.Plan.Raw)
				assertSinglePrincipal(t, final, c.key)
				assertPlanRefinesPrior(t, planned, final)
				if finalID.ValueString() != originGrantID(h.resource(), c.key) {
					t.Fatalf("apply-time id = %v", finalID)
				}
				createResp := &resource.CreateResponse{State: tfsdk.State{Schema: sch}}
				h.Create(ctx, resource.CreateRequest{Plan: applyResp.Plan}, createResp)
				if createResp.Diagnostics.HasError() {
					t.Fatalf("create diagnostics: %v", createResp.Diagnostics)
				}
				grant := c.key.wire()
				grant.Permission = h.wirePermission()
				if body, want := mock.lastBody("POST "+h.route()), wireJSON(t, grant); body != want {
					t.Fatalf("create body = %s, want %s", body, want)
				}
			})
		}
	}
}

func TestOriginGrantCreateRejectsStillUnknownPrincipalID(t *testing.T) {
	mock := newGrantMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originRepoGrantResource{client: mock.client("", "")}
	plan := sampleRepoGrantModel()
	plan.ID = types.StringUnknown()
	plan.User = types.ObjectValueMust(originGrantUserAttrTypes, map[string]attr.Value{"id": types.StringUnknown()})
	planValue := tfsdk.Plan{Schema: repoGrantSchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: planValue.Schema}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "user.id is incomplete") {
		t.Fatalf("diagnostics = %v, want incomplete principal error", resp.Diagnostics)
	}
	if mock.count("POST /repos/acme/rocket/grants") != 0 {
		t.Fatal("nothing may be written for an unknown principal")
	}
}

// A configuration naming two principals or none is a plan error, never silently accepted.
func TestOriginGrantModifyPlanRejectsInvalidConfiguredPrincipal(t *testing.T) {
	ctx := context.Background()
	for name, newHarness := range grantHarnesses() {
		for _, c := range []struct {
			name   string
			config originGrantPrincipal
		}{
			{"two principals", nullPrincipal().withUser(principalObject(originGrantUserAttrTypes, "id", "user_01")).withGroup(principalObject(originGrantGroupAttrTypes, "id", "grp_01"))},
			{"no principal", nullPrincipal()},
		} {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				mock := newGrantMock(t)
				defer mock.Close()
				h := newHarness(mock.client("", ""))
				sch := h.schema(t)
				config := tfsdk.Config{Schema: sch, Raw: h.raw(t, c.config, types.StringNull())}
				plan := tfsdk.Plan{Schema: sch, Raw: h.raw(t, proposedPrincipal(c.config, nil), types.StringUnknown())}
				resp := &resource.ModifyPlanResponse{Plan: plan}
				h.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: config, Plan: plan, State: emptyState(ctx, sch)}, resp)
				if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "exactly one") {
					t.Fatalf("diagnostics = %v, want exactly-one principal error", resp.Diagnostics)
				}
			})
		}
	}
}

type grantPrincipalCase struct {
	family string
	config originGrantPrincipal
	key    originGrantKey
}

// One configuration per principal family; the mock resolves alice@acme.com to user_alice and Engineering to grp_eng.
func grantPrincipalCases() []grantPrincipalCase {
	return []grantPrincipalCase{
		{principalFamilyUserEmail, nullPrincipal().withUserEmail("alice@acme.com"), originGrantKey{kind: originGrantKindUser, value: "user_alice"}},
		{principalFamilyGroupName, nullPrincipal().withGroupName("Engineering"), originGrantKey{kind: originGrantKindGroup, value: "grp_eng"}},
		{originGrantKindUser, nullPrincipal().withUser(principalObject(originGrantUserAttrTypes, "id", "user_01")), originGrantKey{kind: originGrantKindUser, value: "user_01"}},
		{originGrantKindGroup, nullPrincipal().withGroup(principalObject(originGrantGroupAttrTypes, "id", "grp_01")), originGrantKey{kind: originGrantKindGroup, value: "grp_01"}},
		{originGrantKindTeamGroup, nullPrincipal().withTeamGroup(principalObject(originGrantTeamGroupAttrTypes, "kind", originTeamGroupMembers)), originGrantKey{kind: originGrantKindTeamGroup, value: originTeamGroupMembers}},
	}
}

func nullPrincipal() originGrantPrincipal {
	return originGrantPrincipal{
		UserEmail: types.StringNull(),
		GroupName: types.StringNull(),
		User:      types.ObjectNull(originGrantUserAttrTypes),
		Group:     types.ObjectNull(originGrantGroupAttrTypes),
		TeamGroup: types.ObjectNull(originGrantTeamGroupAttrTypes),
	}
}

func (p originGrantPrincipal) withUserEmail(email string) originGrantPrincipal {
	p.UserEmail = types.StringValue(email)
	return p
}

func (p originGrantPrincipal) withGroupName(name string) originGrantPrincipal {
	p.GroupName = types.StringValue(name)
	return p
}

func (p originGrantPrincipal) withUser(user types.Object) originGrantPrincipal {
	p.User = user
	return p
}

func (p originGrantPrincipal) withGroup(group types.Object) originGrantPrincipal {
	p.Group = group
	return p
}

func (p originGrantPrincipal) withTeamGroup(teamGroup types.Object) originGrantPrincipal {
	p.TeamGroup = teamGroup
	return p
}

// proposedPrincipal mirrors the plan the framework hands ModifyPlan: configured attributes as written, and the
// computed user and group unknown on create or, with prior state, copied from it by UseStateForUnknown whenever the
// configuration leaves them null, whichever family the configuration now uses.
func proposedPrincipal(config originGrantPrincipal, prior *originGrantPrincipal) originGrantPrincipal {
	p := config
	if p.User.IsNull() {
		p.User = types.ObjectUnknown(originGrantUserAttrTypes)
		if prior != nil {
			p.User = prior.User
		}
	}
	if p.Group.IsNull() {
		p.Group = types.ObjectUnknown(originGrantGroupAttrTypes)
		if prior != nil {
			p.Group = prior.Group
		}
	}
	return p
}

func assertSinglePrincipal(t *testing.T, p originGrantPrincipal, key originGrantKey) {
	t.Helper()
	for kind, value := range map[string]types.String{
		originGrantKindUser:      principalField(p.User, "id"),
		originGrantKindGroup:     principalField(p.Group, "id"),
		originGrantKindTeamGroup: principalField(p.TeamGroup, "kind"),
	} {
		if kind == key.kind {
			if value.IsNull() || value.IsUnknown() || value.ValueString() != key.value {
				t.Fatalf("%s = %v, want %q", kind, value, key.value)
			}
			continue
		}
		if !value.IsNull() {
			t.Fatalf("%s = %v, want null next to %s", kind, value, key)
		}
	}
}

// Terraform only lets an apply-time plan fill in values the initial plan left unknown.
func assertPlanRefinesPrior(t *testing.T, initial, final originGrantPrincipal) {
	t.Helper()
	check := func(name string, before, after types.Object) {
		if before.IsUnknown() {
			return
		}
		if before.IsNull() != after.IsNull() {
			t.Fatalf("%s: apply-time plan %v does not refine the initial plan %v", name, after, before)
		}
		for field, value := range before.Attributes() {
			if !value.IsUnknown() && !value.Equal(after.Attributes()[field]) {
				t.Fatalf("%s.%s: apply-time plan %v does not refine the initial plan %v", name, field, after, before)
			}
		}
	}
	check("user", initial.User, final.User)
	check("group", initial.Group, final.Group)
	check("team_group", initial.TeamGroup, final.TeamGroup)
}

func wireJSON(t *testing.T, grant originGrant) string {
	t.Helper()
	raw, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// grantHarness drives one grant resource through plan and apply with values built from a principal.
type grantHarness interface {
	resource.ResourceWithModifyPlan
	schema(t *testing.T) schema.Schema
	raw(t *testing.T, p originGrantPrincipal, id types.String) tftypes.Value
	principal(t *testing.T, value tftypes.Value) (originGrantPrincipal, types.String)
	resource() string
	route() string
	wirePermission() string
}

func grantHarnesses() map[string]func(*apiClient) grantHarness {
	return map[string]func(*apiClient) grantHarness{
		"repo": func(client *apiClient) grantHarness {
			return repoGrantHarness{&originRepoGrantResource{client: client}}
		},
		"owner": func(client *apiClient) grantHarness {
			return ownerGrantHarness{&originOwnerGrantResource{client: client}}
		},
	}
}

type repoGrantHarness struct{ *originRepoGrantResource }

func (h repoGrantHarness) schema(t *testing.T) schema.Schema {
	return repoGrantSchema(t, h.originRepoGrantResource)
}

func (h repoGrantHarness) raw(t *testing.T, p originGrantPrincipal, id types.String) tftypes.Value {
	t.Helper()
	model := sampleRepoGrantModel().withPrincipal(p)
	model.ID = id
	value := tfsdk.Plan{Schema: h.schema(t)}
	if diags := value.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return value.Raw
}

func (h repoGrantHarness) principal(t *testing.T, value tftypes.Value) (originGrantPrincipal, types.String) {
	t.Helper()
	var model originRepoGrantModel
	if diags := (tfsdk.State{Schema: h.schema(t), Raw: value}).Get(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return model.principal(), model.ID
}

func (h repoGrantHarness) resource() string       { return "acme/rocket" }
func (h repoGrantHarness) route() string          { return "/repos/acme/rocket/grants" }
func (h repoGrantHarness) wirePermission() string { return repoPermissionToWire(originPermissionWrite) }

type ownerGrantHarness struct{ *originOwnerGrantResource }

func (h ownerGrantHarness) schema(t *testing.T) schema.Schema {
	return ownerGrantSchema(t, h.originOwnerGrantResource)
}

func (h ownerGrantHarness) raw(t *testing.T, p originGrantPrincipal, id types.String) tftypes.Value {
	t.Helper()
	model := sampleOwnerTeamGrantModel(originTeamGroupMembers, originPermissionRead).withPrincipal(p)
	model.ID = id
	value := tfsdk.Plan{Schema: h.schema(t)}
	if diags := value.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return value.Raw
}

func (h ownerGrantHarness) principal(t *testing.T, value tftypes.Value) (originGrantPrincipal, types.String) {
	t.Helper()
	var model originOwnerGrantModel
	if diags := (tfsdk.State{Schema: h.schema(t), Raw: value}).Get(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return model.principal(), model.ID
}

func (h ownerGrantHarness) resource() string { return "acme" }
func (h ownerGrantHarness) route() string    { return "/owners/acme/grants" }
func (h ownerGrantHarness) wirePermission() string {
	return ownerPermissionToWire(originPermissionRead)
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

// Configuration behind a plan: only the attributes written in HCL, never the computed id.
func repoGrantConfig(t *testing.T, sch schema.Schema, model originRepoGrantModel) tfsdk.Config {
	t.Helper()
	model.ID = types.StringNull()
	value := tfsdk.Plan{Schema: sch}
	if diags := value.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return tfsdk.Config{Schema: sch, Raw: value.Raw}
}

func ownerGrantConfig(t *testing.T, sch schema.Schema, model originOwnerGrantModel) tfsdk.Config {
	t.Helper()
	model.ID = types.StringNull()
	value := tfsdk.Plan{Schema: sch}
	if diags := value.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return tfsdk.Config{Schema: sch, Raw: value.Raw}
}

func repoGrantState(t *testing.T, res *originRepoGrantResource, model originRepoGrantModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: repoGrantSchema(t, res)}
	if diags := state.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return state
}
