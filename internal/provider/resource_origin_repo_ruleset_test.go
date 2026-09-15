package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestOriginRulesetCreateUpdateDelete(t *testing.T) {
	var created originRulesetWrite
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer session-token" {
			t.Errorf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/rocket/rulesets":
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			writeJSON(t, w, http.StatusOK, sampleOriginRuleset())
		case r.Method == http.MethodPut && r.URL.Path == "/repos/acme/rocket/rulesets/rs_01":
			var updated originRulesetWrite
			if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
				t.Errorf("decode update body: %v", err)
			}
			if updated.Enforcement != originRulesetEnforcementEvaluate {
				t.Errorf("update enforcement = %q", updated.Enforcement)
			}
			if len(updated.Rules) != 1 || string(updated.Rules[0].Parameters) != `{"requiredApprovingReviewCount":1}` {
				t.Fatalf("update rules = %#v", updated.Rules)
			}
			ruleset := sampleOriginRuleset()
			ruleset.Enforcement = originRulesetEnforcementEvaluate
			writeJSON(t, w, http.StatusOK, ruleset)
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/acme/rocket/rulesets/rs_01":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/rocket/rulesets/rs_01":
			writeJSON(t, w, http.StatusOK, sampleOriginRuleset())
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := testOriginClient(server)
	createdRuleset, err := client.createOriginRuleset(context.Background(), "acme", "rocket", sampleOriginRulesetWrite())
	if err != nil {
		t.Fatalf("createOriginRuleset() error: %v", err)
	}
	if createdRuleset.ID != "rs_01" {
		t.Fatalf("id = %q", createdRuleset.ID)
	}
	if created.Name != "require-review" || created.Kind != originRulesetKindMergeBranch {
		t.Fatalf("create body = %#v", created)
	}
	if len(created.IncludedRefNames) != 1 || created.IncludedRefNames[0] != "refs/heads/main" {
		t.Fatalf("included = %#v", created.IncludedRefNames)
	}
	if len(created.Rules) != 1 || string(created.Rules[0].Parameters) != `{"requiredApprovingReviewCount":1}` {
		t.Fatalf("rules = %#v", created.Rules)
	}
	if created.BypassActors[0].User == nil || created.BypassActors[0].User.ID != "act_01k2ja2000e0080000000000u1" {
		t.Fatalf("bypass = %#v", created.BypassActors)
	}

	updatedBody := sampleOriginRulesetWrite()
	updatedBody.Enforcement = originRulesetEnforcementEvaluate
	updated, err := client.updateOriginRuleset(context.Background(), "acme", "rocket", "rs_01", updatedBody)
	if err != nil {
		t.Fatalf("updateOriginRuleset() error: %v", err)
	}
	if updated.Enforcement != originRulesetEnforcementEvaluate {
		t.Errorf("enforcement = %q", updated.Enforcement)
	}

	if err := client.deleteOriginRuleset(context.Background(), "acme", "rocket", "rs_01"); err != nil {
		t.Fatalf("deleteOriginRuleset() error: %v", err)
	}
}

func TestOriginRulesetModelMatchesSchema(t *testing.T) {
	r := &originRepoRulesetResource{}
	ctx := context.Background()
	schemaResp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", schemaResp.Diagnostics)
	}

	stateModel, err := originRulesetToModel(ctx, sampleOriginRulesetModel(), sampleOriginRuleset(), true)
	if err != nil {
		t.Fatal(err)
	}
	state := &tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(ctx, &stateModel); diags.HasError() {
		t.Fatalf("state.Set: %v", diags)
	}

	var decoded originRepoRulesetModel
	if diags := state.Get(ctx, &decoded); diags.HasError() {
		t.Fatalf("state.Get: %v", diags)
	}
	if decoded.ID.ValueString() != "rs_01" || decoded.Rules[0].RuleType.ValueString() != "pull_request" {
		t.Fatalf("decoded = %#v", decoded)
	}
	if decoded.BypassActors[0].Team != nil || decoded.BypassActors[0].App != nil || decoded.BypassActors[0].OriginRole != nil {
		t.Fatalf("unset bypass principals should be null: %#v", decoded.BypassActors[0])
	}
}

func TestOriginRulesetReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, originStatusError{Code: 5, Message: "missing"})
	}))
	defer server.Close()

	_, err := testOriginClient(server).getOriginRuleset(context.Background(), "acme", "rocket", "rs_missing")
	if !isOriginNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
}

func TestOriginRulesetToModelPreservesConfiguredIdentity(t *testing.T) {
	plan := originRepoRulesetModel{
		Owner: types.StringValue("Acme"),
		Repo:  types.StringValue("Rocket"),
		Name:  types.StringValue("Require-Review"),
		Rules: []originRepoRulesetRuleModel{{
			RuleType:   types.StringValue("pull_request"),
			Parameters: jsonObjectValueOf(`{"requiredApprovingReviewCount":1}`),
		}},
		BypassActors: []originRepoRulesetBypassModel{{
			BypassMode: types.StringValue(originBypassModeAlways),
			User:       &originRepoRulesetUserModel{ID: types.StringValue("act_01k2ja2000e0080000000000u1")},
		}},
	}
	state, err := originRulesetToModel(context.Background(), plan, sampleOriginRuleset(), true)
	if err != nil {
		t.Fatalf("originRulesetToModel() error: %v", err)
	}
	if state.Owner.ValueString() != "Acme" || state.Repo.ValueString() != "Rocket" {
		t.Fatalf("owner/repo = %s/%s", state.Owner.ValueString(), state.Repo.ValueString())
	}
	if state.Name.ValueString() != "require-review" {
		t.Errorf("name = %q, want API name", state.Name.ValueString())
	}
	if state.ID.ValueString() != "rs_01" {
		t.Errorf("id = %q", state.ID.ValueString())
	}
	if len(state.Rules) != 1 || state.Rules[0].ID.ValueString() != "rsr_01" {
		t.Fatalf("rules = %#v", state.Rules)
	}
	if state.Rules[0].Parameters.ValueString() != `{"requiredApprovingReviewCount":1}` {
		t.Errorf("parameters = %q", state.Rules[0].Parameters.ValueString())
	}
	if len(state.BypassActors) != 1 || state.BypassActors[0].User == nil || state.BypassActors[0].User.ID.ValueString() != "act_01k2ja2000e0080000000000u1" {
		t.Fatalf("bypass = %#v", state.BypassActors)
	}
}

func TestValidateOriginRulesetModel(t *testing.T) {
	valid := sampleOriginRulesetModel()
	if err := validateOriginRulesetModel(context.Background(), valid); err != nil {
		t.Fatalf("validateOriginRulesetModel() unexpected error: %v", err)
	}

	missingActor := valid
	missingActor.BypassActors = []originRepoRulesetBypassModel{{
		BypassMode: types.StringValue(originBypassModeAlways),
	}}
	if err := validateOriginRulesetModel(context.Background(), missingActor); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("error = %v, want exactly one actor", err)
	}

	twoActors := valid
	twoActors.BypassActors = []originRepoRulesetBypassModel{{
		BypassMode: types.StringValue(originBypassModeAlways),
		User:       &originRepoRulesetUserModel{ID: types.StringUnknown()},
		App:        &originRepoRulesetAppModel{ID: types.StringValue("app_01")},
	}}
	if err := validateOriginRulesetModel(context.Background(), twoActors); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("error = %v, want exactly one actor", err)
	}

	padded := valid
	padded.Enforcement = types.StringValue(" active")
	if err := validateOriginRulesetModel(context.Background(), padded); err == nil || !strings.Contains(err.Error(), "spaces") {
		t.Fatalf("error = %v, want padded enforcement rejected", err)
	}

	badKind := valid
	badKind.Kind = types.StringValue("branch")
	if err := validateOriginRulesetModel(context.Background(), badKind); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("error = %v, want kind error", err)
	}

	badParams := valid
	badParams.Rules = []originRepoRulesetRuleModel{{
		RuleType:   types.StringValue("pull_request"),
		Parameters: jsonObjectValueOf(`["nope"]`),
	}}
	if err := validateOriginRulesetModel(context.Background(), badParams); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("error = %v, want JSON object error", err)
	}

	emptyRules := valid
	emptyRules.Rules = nil
	if err := validateOriginRulesetModel(context.Background(), emptyRules); err == nil || !strings.Contains(err.Error(), "allow_unenforced") {
		t.Fatalf("error = %v, want empty rules rejected", err)
	}

	disabled := valid
	disabled.Enforcement = types.StringValue(originRulesetEnforcementDisabled)
	if err := validateOriginRulesetModel(context.Background(), disabled); err == nil || !strings.Contains(err.Error(), "allow_unenforced") {
		t.Fatalf("error = %v, want disabled enforcement rejected", err)
	}
	disabled.AllowUnenforced = types.BoolValue(true)
	if err := validateOriginRulesetModel(context.Background(), disabled); err != nil {
		t.Fatalf("disabled ruleset with allow_unenforced: %v", err)
	}

	excludeAll := valid
	excludedAll, err := stringListValue(context.Background(), []string{"~ALL"})
	if err != nil {
		t.Fatal(err)
	}
	excludeAll.ExcludedRefNames = excludedAll
	if err := validateOriginRulesetModel(context.Background(), excludeAll); err == nil || !strings.Contains(err.Error(), "~ALL") {
		t.Fatalf("error = %v, want ~ALL exclusion rejected", err)
	}

	writeRole := valid
	writeRole.BypassActors = []originRepoRulesetBypassModel{{
		BypassMode: types.StringValue(originBypassModeAlways),
		OriginRole: &originRepoRulesetOriginRoleModel{Role: types.StringValue(originRoleRepositoryWrite)},
	}}
	if err := validateOriginRulesetModel(context.Background(), writeRole); err == nil || !strings.Contains(err.Error(), "allow_broad_bypass") {
		t.Fatalf("error = %v, want repository_write bypass rejected", err)
	}
	writeRole.AllowBroadBypass = types.BoolValue(true)
	if err := validateOriginRulesetModel(context.Background(), writeRole); err != nil {
		t.Fatalf("repository_write bypass with allow_broad_bypass: %v", err)
	}

	wildcard := valid
	wildcard.BypassActors = []originRepoRulesetBypassModel{{
		BypassMode: types.StringValue(originBypassModeAlways),
		User:       &originRepoRulesetUserModel{ID: types.StringValue("~ALL")},
	}}
	if err := validateOriginRulesetModel(context.Background(), wildcard); err == nil || !strings.Contains(err.Error(), "not a principal ID") {
		t.Fatalf("error = %v, want wildcard user id rejected", err)
	}
}

func TestParseOriginRulesetImportID(t *testing.T) {
	owner, repo, id, err := parseOriginRulesetImportID("acme/rocket/rs_01")
	if err != nil {
		t.Fatal(err)
	}
	if owner != "acme" || repo != "rocket" || id != "rs_01" {
		t.Fatalf("got %s/%s/%s", owner, repo, id)
	}
	if _, _, _, err := parseOriginRulesetImportID("acme/rocket"); err == nil || !strings.Contains(err.Error(), "acme/rocket") {
		t.Fatalf("error = %v, want import ID in the message", err)
	}
}

func TestCanonicalJSONObjectIgnoresKeyOrder(t *testing.T) {
	left, err := canonicalJSONObject(`{"b":1,"a":{"z":true,"y":"x"}}`)
	if err != nil {
		t.Fatal(err)
	}
	right, err := canonicalJSONObject(" { \"a\" : { \"y\" : \"x\", \"z\" : true }, \"b\" : 1 } ")
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("canonical mismatch\n%s\n%s", left, right)
	}
}

func sampleOriginRulesetWrite() originRulesetWrite {
	return originRulesetWrite{
		Name:             "require-review",
		Description:      "Require an approving review before merging to main.",
		Enforcement:      originRulesetEnforcementActive,
		Kind:             originRulesetKindMergeBranch,
		IncludedRefNames: []string{"refs/heads/main"},
		ExcludedRefNames: []string{},
		Rules: []originRulesetRuleInput{{
			RuleType:   "pull_request",
			Parameters: json.RawMessage(`{"requiredApprovingReviewCount":1}`),
		}},
		BypassActors: []originRulesetBypassActorInput{{
			BypassMode: originBypassModeAlways,
			User:       &originRulesetUserBypass{ID: "act_01k2ja2000e0080000000000u1"},
		}},
	}
}

func sampleOriginRuleset() *originRuleset {
	return &originRuleset{
		ID:               "rs_01",
		Name:             "require-review",
		Description:      "Require an approving review before merging to main.",
		Enforcement:      originRulesetEnforcementActive,
		Kind:             originRulesetKindMergeBranch,
		IncludedRefNames: []string{"refs/heads/main"},
		ExcludedRefNames: []string{},
		Rules: []originRulesetRule{{
			ID:         "rsr_01",
			RuleType:   "pull_request",
			Parameters: json.RawMessage(`{"requiredApprovingReviewCount":1}`),
		}},
		BypassActors: []originRulesetBypassActor{{
			ID:         "rsba_01",
			BypassMode: originBypassModeAlways,
			User:       &originRulesetUserBypass{ID: "act_01k2ja2000e0080000000000u1"},
		}},
	}
}

func sampleOriginRulesetModel() originRepoRulesetModel {
	included, err := stringListValue(context.Background(), []string{"refs/heads/main"})
	if err != nil {
		panic(err)
	}
	excluded, err := stringListValue(context.Background(), nil)
	if err != nil {
		panic(err)
	}
	return originRepoRulesetModel{
		Owner:            types.StringValue("acme"),
		Repo:             types.StringValue("rocket"),
		Name:             types.StringValue("require-review"),
		Description:      types.StringValue("Require an approving review before merging to main."),
		Enforcement:      types.StringValue(originRulesetEnforcementActive),
		Kind:             types.StringValue(originRulesetKindMergeBranch),
		IncludedRefNames: included,
		ExcludedRefNames: excluded,
		Rules: []originRepoRulesetRuleModel{{
			RuleType:   types.StringValue("pull_request"),
			Parameters: jsonObjectValueOf(`{"requiredApprovingReviewCount":1}`),
		}},
		BypassActors: []originRepoRulesetBypassModel{{
			BypassMode: types.StringValue(originBypassModeAlways),
			User:       &originRepoRulesetUserModel{ID: types.StringValue("act_01k2ja2000e0080000000000u1")},
		}},
	}
}

func TestOriginRulesetApplyUsesResponseIDs(t *testing.T) {
	prior := sampleOriginRulesetModel()
	prior.ID = types.StringValue("rs_old")
	prior.Rules[0].ID = types.StringValue("rsr_old")
	prior.BypassActors[0].ID = types.StringValue("rsba_old")

	api := sampleOriginRuleset()
	api.Rules[0].ID = "rsr_new"
	api.BypassActors[0].ID = "rsba_new"
	api.Rules[0].Parameters = json.RawMessage(`{"requiredApprovingReviewCount":1,"extra":true}`)

	state, err := originRulesetToModel(context.Background(), prior, api, true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Rules[0].ID.ValueString() != "rsr_new" {
		t.Fatalf("rule id = %q, want response id", state.Rules[0].ID.ValueString())
	}
	if state.BypassActors[0].ID.ValueString() != "rsba_new" {
		t.Fatalf("bypass id = %q, want response id", state.BypassActors[0].ID.ValueString())
	}
	if state.Rules[0].Parameters.ValueString() != prior.Rules[0].Parameters.ValueString() {
		t.Fatalf("parameters = %q, want planned value", state.Rules[0].Parameters.ValueString())
	}
}

func TestOriginRulesetNullParametersStayNull(t *testing.T) {
	prior := sampleOriginRulesetModel()
	prior.Rules[0].Parameters = jsonObjectNull()

	state, err := originRulesetToModel(context.Background(), prior, sampleOriginRuleset(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Rules[0].Parameters.IsNull() {
		t.Fatalf("parameters = %q, want null", state.Rules[0].Parameters.ValueString())
	}
}

func TestOriginRulesetOmittedBypassActorErrors(t *testing.T) {
	api := sampleOriginRuleset()
	api.BypassActors = nil

	_, err := originRulesetToModel(context.Background(), sampleOriginRulesetModel(), api, true)
	if err == nil || !strings.Contains(err.Error(), "omitted requested bypass actor") {
		t.Fatalf("error = %v, want omitted bypass actor", err)
	}
}

func TestOriginRulesetWriteOmitsNestedIDsAndSendsEmptySlices(t *testing.T) {
	model := sampleOriginRulesetModel()
	model.ID = types.StringValue("rs_01")
	model.Rules[0].ID = types.StringValue("rsr_01")
	model.BypassActors[0].ID = types.StringValue("rsba_01")

	body, err := rulesetWriteFromModel(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["excludedRefNames"].([]any); !ok || len(payload["excludedRefNames"].([]any)) != 0 {
		t.Fatalf("excludedRefNames = %#v", payload["excludedRefNames"])
	}
	rules, _ := payload["rules"].([]any)
	actors, _ := payload["bypassActors"].([]any)
	if len(rules) != 1 || len(actors) != 1 {
		t.Fatalf("rules/actors = %#v %#v", rules, actors)
	}
	if _, ok := rules[0].(map[string]any)["id"]; ok {
		t.Fatalf("rule write includes id: %#v", rules[0])
	}
	if _, ok := actors[0].(map[string]any)["id"]; ok {
		t.Fatalf("bypass actor write includes id: %#v", actors[0])
	}
}

func TestOriginRulesetMatchesBypassActorsByPrincipal(t *testing.T) {
	prior := sampleOriginRulesetModel()
	prior.BypassActors = []originRepoRulesetBypassModel{
		{
			ID:         types.StringValue("rsba_user_old"),
			BypassMode: types.StringValue(originBypassModeAlways),
			User:       &originRepoRulesetUserModel{ID: types.StringValue("act_01k2ja2000e0080000000000u1")},
		},
		{
			ID:         types.StringValue("rsba_app_old"),
			BypassMode: types.StringValue(originBypassModePullRequestOnly),
			App:        &originRepoRulesetAppModel{ID: types.StringValue("app_01")},
		},
	}
	api := sampleOriginRuleset()
	api.BypassActors = []originRulesetBypassActor{
		{
			ID:         "rsba_app_new",
			BypassMode: originBypassModePullRequestOnly,
			App:        &originRulesetAppBypass{ID: "app_01"},
		},
		{
			ID:         "rsba_user_new",
			BypassMode: originBypassModeAlways,
			User:       &originRulesetUserBypass{ID: "act_01k2ja2000e0080000000000u1"},
		},
	}

	state, err := originRulesetToModel(context.Background(), prior, api, true)
	if err != nil {
		t.Fatal(err)
	}
	if state.BypassActors[0].ID.ValueString() != "rsba_user_new" || state.BypassActors[0].User == nil {
		t.Fatalf("user actor = %#v", state.BypassActors[0])
	}
	if state.BypassActors[1].ID.ValueString() != "rsba_app_new" || state.BypassActors[1].App == nil {
		t.Fatalf("app actor = %#v", state.BypassActors[1])
	}
}

func TestNestedRulesetIDsHaveNoPlanModifiers(t *testing.T) {
	r := &originRepoRulesetResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, schemaResp)

	rule := schemaResp.Schema.Attributes["rule"].(schema.ListNestedAttribute)
	ruleID := rule.NestedObject.Attributes["id"].(schema.StringAttribute)
	if len(ruleID.PlanModifiers) != 0 {
		t.Fatalf("rule.id plan modifiers = %d, want 0", len(ruleID.PlanModifiers))
	}
	actor := schemaResp.Schema.Attributes["bypass_actor"].(schema.ListNestedAttribute)
	actorID := actor.NestedObject.Attributes["id"].(schema.StringAttribute)
	if len(actorID.PlanModifiers) != 0 {
		t.Fatalf("bypass_actor.id plan modifiers = %d, want 0", len(actorID.PlanModifiers))
	}
}

func TestOriginRulesetReadNotFoundKeepsState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, originStatusError{Code: 5, Message: "missing"})
	}))
	defer server.Close()

	ctx := context.Background()
	res := &originRepoRulesetResource{client: testOriginClient(server)}
	schemaResp := &resource.SchemaResponse{}
	res.Schema(ctx, resource.SchemaRequest{}, schemaResp)

	model := sampleOriginRulesetModel()
	model.ID = types.StringValue("rs_missing")
	state := tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}

	resp := &resource.ReadResponse{State: state}
	res.Read(ctx, resource.ReadRequest{State: state}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected read error")
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("read removed state on 404")
	}
}

func TestOriginRulesetDeleteProtection(t *testing.T) {
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := context.Background()
	res := &originRepoRulesetResource{client: testOriginClient(server)}
	schemaResp := &resource.SchemaResponse{}
	res.Schema(ctx, resource.SchemaRequest{}, schemaResp)

	protected := sampleOriginRulesetModel()
	protected.ID = types.StringValue("rs_01")
	protected.DeletionProtection = types.BoolValue(true)
	state := tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(ctx, &protected); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: state}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected deletion_protection to block delete")
	}
	if deleted {
		t.Fatal("delete request was sent while protected")
	}

	unprotected := protected
	unprotected.DeletionProtection = types.BoolValue(false)
	state = tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(ctx, &unprotected); diags.HasError() {
		t.Fatal(diags)
	}
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unprotected delete: %v", resp.Diagnostics)
	}
	if !deleted {
		t.Fatal("expected delete request when deletion_protection is false")
	}
}

func TestRulesetReplacementGuard(t *testing.T) {
	state := sampleOriginRulesetModel()
	state.DeletionProtection = types.BoolValue(true)
	plan := state
	plan.Owner = types.StringValue("other")
	if err := rulesetReplacementGuard(state, plan); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("error = %v, want owner replacement blocked", err)
	}

	plan = state
	plan.Kind = types.StringValue(originRulesetKindPushTag)
	if err := rulesetReplacementGuard(state, plan); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("error = %v, want kind change blocked", err)
	}

	state.DeletionProtection = types.BoolValue(false)
	if err := rulesetReplacementGuard(state, plan); err != nil {
		t.Fatalf("unprotected replacement: %v", err)
	}
}

func TestOriginRulesetDeleteNotFoundIsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	err := testOriginClient(server).deleteOriginRuleset(context.Background(), "acme", "rocket", "rs_01")
	if !isOriginNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
}
