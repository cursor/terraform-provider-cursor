package provider

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestOriginSSHCertificateAuthorityCreateKeepsConfiguredLine(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()

	res := &originSSHCertificateAuthorityResource{client: mock.client()}
	plan := sampleSSHCAModel()
	plan.ID, plan.KeyType, plan.Fingerprint, plan.CreatedAt = types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	plan.PublicKey = types.StringValue(sampleSSHCAKey + " acme-ssh-ca\n")
	got := createSSHCA(t, res, plan)
	if body := mock.lastBody("POST /namespaces/acme/ssh-certificate-authorities"); body != `{"publicKey":"`+sampleSSHCAKey+` acme-ssh-ca","name":"Acme production CA"}` {
		t.Fatalf("create body = %s", body)
	}
	if got.PublicKey.ValueString() != sampleSSHCAKey+" acme-ssh-ca\n" {
		t.Fatalf("public_key = %q, want the configured line kept", got.PublicKey.ValueString())
	}
	if got.ID.ValueString() != "nsca_00000000000000000000000001" || got.KeyType.ValueString() != "ssh-ed25519" || got.Fingerprint.ValueString() != sampleSSHCAFingerprint || got.CreatedAt.ValueString() != "2026-08-02T14:45:01Z" {
		t.Fatalf("state = %#v", got)
	}
}

func TestOriginSSHCertificateAuthorityCreateSurfacesAPIErrors(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	mock.add("acme", sampleSSHCAKey, "existing")

	ctx := context.Background()
	res := &originSSHCertificateAuthorityResource{client: mock.client()}
	plan := sampleSSHCAModel()
	plan.ID = types.StringUnknown()
	planValue := tfsdk.Plan{Schema: sshCASchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: planValue.Schema}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "HTTP 409") {
		t.Fatalf("diagnostics = %v, want the 409 surfaced", resp.Diagnostics)
	}
}

func TestOriginSSHCertificateAuthorityReadRefreshesFromList(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	mock.add("acme", secondSSHCAKey, "other")
	stored := mock.add("acme", sampleSSHCAKey, "Renamed in the portal")

	res := &originSSHCertificateAuthorityResource{client: mock.client()}
	model := sampleSSHCAModel()
	model.ID = types.StringValue(stored.ID)
	model.PublicKey = types.StringValue(sampleSSHCAKey + " acme-ssh-ca")
	got := readSSHCA(t, res, model)
	if got == nil {
		t.Fatal("read removed a listed authority")
	}
	if got.Name.ValueString() != "Renamed in the portal" || got.PublicKey.ValueString() != sampleSSHCAKey+" acme-ssh-ca" || got.Fingerprint.ValueString() != sampleSSHCAFingerprint {
		t.Fatalf("state = %#v", got)
	}
}

func TestOriginSSHCertificateAuthorityReadFillsImportedKey(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	stored := mock.add("acme", sampleSSHCAKey, "Acme production CA")

	res := &originSSHCertificateAuthorityResource{client: mock.client()}
	got := readSSHCA(t, res, originSSHCertificateAuthorityModel{
		ID:                 types.StringValue(stored.ID),
		Namespace:          types.StringValue("acme"),
		Owner:              types.StringValue("acme"),
		Name:               types.StringNull(),
		PublicKey:          types.StringNull(),
		KeyType:            types.StringNull(),
		Fingerprint:        types.StringNull(),
		CreatedAt:          types.StringNull(),
		DeletionProtection: types.BoolNull(),
	})
	if got == nil || got.PublicKey.ValueString() != sampleSSHCAKey || got.Name.ValueString() != "Acme production CA" || got.KeyType.ValueString() != "ssh-ed25519" {
		t.Fatalf("state = %#v", got)
	}
	if !got.DeletionProtection.ValueBool() {
		t.Fatal("a null deletion_protection must read back as protected")
	}
}

func TestOriginSSHCertificateAuthorityReadRemovesMissing(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	mock.add("acme", secondSSHCAKey, "other")

	res := &originSSHCertificateAuthorityResource{client: mock.client()}
	if got := readSSHCA(t, res, sampleSSHCAModel()); got != nil {
		t.Fatalf("expected missing authority to be removed from state, got %#v", got)
	}
}

func TestOriginSSHCertificateAuthorityReadListErrorKeepsState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, originStatusError{Code: 5, Message: "missing"})
	}))
	defer server.Close()

	res := &originSSHCertificateAuthorityResource{client: testOriginClient(server)}
	state := sshCAState(t, res, sampleSSHCAModel())
	resp := &resource.ReadResponse{State: state}
	res.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected read error when the namespace cannot be listed")
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("read removed state on list failure")
	}
}

func TestOriginSSHCertificateAuthorityUpdateRecordsCommentOnly(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originSSHCertificateAuthorityResource{client: mock.client()}
	sch := sshCASchema(t, res)
	state := sampleSSHCAModel()
	plan := state
	plan.PublicKey = types.StringValue(sampleSSHCAKey + " renamed-comment")
	plan.ID, plan.KeyType, plan.Fingerprint, plan.CreatedAt = types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.StringUnknown()

	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
	res.Update(ctx, resource.UpdateRequest{Plan: planValue, State: sshCAState(t, res, state)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update diagnostics: %v", resp.Diagnostics)
	}
	var got originSSHCertificateAuthorityModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if got.PublicKey.ValueString() != sampleSSHCAKey+" renamed-comment" || got.ID.ValueString() != sampleSSHCAID || got.Fingerprint.ValueString() != sampleSSHCAFingerprint {
		t.Fatalf("state = %#v", got)
	}
	if mock.count("POST /namespaces/acme/ssh-certificate-authorities") != 0 {
		t.Fatal("update must not call the API")
	}

	for name, mutate := range map[string]func(*originSSHCertificateAuthorityModel){
		"changed key":  func(m *originSSHCertificateAuthorityModel) { m.PublicKey = types.StringValue(secondSSHCAKey) },
		"changed name": func(m *originSSHCertificateAuthorityModel) { m.Name = types.StringValue("Renamed") },
	} {
		rejected := state
		mutate(&rejected)
		if diags := planValue.Set(ctx, &rejected); diags.HasError() {
			t.Fatal(diags)
		}
		resp = &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
		res.Update(ctx, resource.UpdateRequest{Plan: planValue, State: sshCAState(t, res, state)}, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatalf("expected %s to be rejected in Update", name)
		}
	}
}

func TestOriginSSHCertificateAuthorityModifyPlanRefusesRename(t *testing.T) {
	ctx := context.Background()
	res := &originSSHCertificateAuthorityResource{}
	sch := sshCASchema(t, res)
	state := sampleSSHCAModel()

	modify := func(plan originSSHCertificateAuthorityModel) *resource.ModifyPlanResponse {
		planValue := tfsdk.Plan{Schema: sch}
		if diags := planValue.Set(ctx, &plan); diags.HasError() {
			t.Fatal(diags)
		}
		resp := &resource.ModifyPlanResponse{Plan: planValue}
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, Config: sshCAConfig(t, sch, plan), State: sshCAState(t, res, state)}, resp)
		return resp
	}

	renamed := state
	renamed.Name = types.StringValue("Renamed in config")
	resp := modify(renamed)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), `name stays "Acme production CA"`) {
		t.Fatalf("diagnostics = %v, want rename refused", resp.Diagnostics)
	}

	commentOnly := state
	commentOnly.PublicKey = types.StringValue(sampleSSHCAKey + " new-comment")
	if resp := modify(commentOnly); resp.Diagnostics.HasError() {
		t.Fatalf("comment-only change must plan: %v", resp.Diagnostics)
	}

	replaced := renamed
	replaced.PublicKey = types.StringValue(secondSSHCAKey)
	if resp := modify(replaced); resp.Diagnostics.HasError() {
		t.Fatalf("rename together with a key change is a replace and must plan: %v", resp.Diagnostics)
	}
	moved := renamed
	moved.Namespace, moved.Owner = types.StringValue("other"), types.StringValue("other")
	if resp := modify(moved); resp.Diagnostics.HasError() {
		t.Fatalf("rename together with a namespace change is a replace and must plan: %v", resp.Diagnostics)
	}
	deferred := renamed
	deferred.Name = types.StringUnknown()
	if resp := modify(deferred); resp.Diagnostics.HasError() {
		t.Fatalf("unknown name is checked at apply, not plan: %v", resp.Diagnostics)
	}

	create := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: sch}}
	if diags := create.Plan.Set(ctx, &renamed); diags.HasError() {
		t.Fatal(diags)
	}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: create.Plan, Config: sshCAConfig(t, sch, renamed), State: emptyState(ctx, sch)}, create)
	if create.Diagnostics.HasError() {
		t.Fatalf("create must plan: %v", create.Diagnostics)
	}
}

func TestOriginSSHCertificateAuthorityPublicKeyReplacesOnlyWhenKeyChanges(t *testing.T) {
	ctx := context.Background()
	res := &originSSHCertificateAuthorityResource{}
	sch := sshCASchema(t, res)
	attribute := sch.Attributes["public_key"].(schema.StringAttribute)
	if len(attribute.PlanModifiers) != 1 {
		t.Fatalf("public_key plan modifiers = %#v", attribute.PlanModifiers)
	}
	state := sshCAState(t, res, sampleSSHCAModel())
	plan := tfsdk.Plan{Schema: sch, Raw: state.Raw}

	run := func(planned string) bool {
		req := planmodifier.StringRequest{
			Path:       path.Root("public_key"),
			Plan:       plan,
			State:      state,
			PlanValue:  types.StringValue(planned),
			StateValue: types.StringValue(sampleSSHCAKey),
		}
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		attribute.PlanModifiers[0].PlanModifyString(ctx, req, resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		return resp.RequiresReplace
	}
	if run(sampleSSHCAKey + " new-comment") {
		t.Fatal("comment change must not replace the authority")
	}
	if !run(secondSSHCAKey) {
		t.Fatal("key change must replace the authority")
	}
}

func TestOriginSSHCertificateSchemas(t *testing.T) {
	ctx := context.Background()
	authority := sshCASchema(t, &originSSHCertificateAuthorityResource{})
	if diags := authority.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("authority schema implementation: %v", diags)
	}
	if len(authority.Attributes["name"].(schema.StringAttribute).PlanModifiers) != 0 {
		t.Fatal("authority.name must not replace; ModifyPlan refuses renames")
	}
	requirement := sshRequirementSchema(t, &originSSHCertificateRequirementResource{})
	if diags := requirement.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("requirement schema implementation: %v", diags)
	}
	if len(requirement.Attributes["require_certificates"].(schema.BoolAttribute).PlanModifiers) != 0 {
		t.Fatal("requirement.require_certificates must update in place")
	}
	for name, sch := range map[string]schema.Schema{"authority": authority, "requirement": requirement} {
		protection := sch.Attributes["deletion_protection"].(schema.BoolAttribute)
		if !protection.Optional || !protection.Computed || protection.Default == nil {
			t.Fatalf("%s.deletion_protection must be optional and default to true, like origin_repo_ruleset", name)
		}
		if sch.Version != 1 {
			t.Fatalf("%s schema version = %d, want 1 so v0 state gains namespace", name, sch.Version)
		}
		namespace := sch.Attributes["namespace"].(schema.StringAttribute)
		if !namespace.Optional || !namespace.Computed || namespace.DeprecationMessage != "" || len(namespace.PlanModifiers) != 2 {
			t.Fatalf("%s.namespace must be optional, keep its state value, and require replace", name)
		}
		owner := sch.Attributes["owner"].(schema.StringAttribute)
		if !owner.Optional || !owner.Computed || owner.DeprecationMessage == "" || len(owner.PlanModifiers) != 2 {
			t.Fatalf("%s.owner must stay an optional deprecated alias that requires replace", name)
		}
	}
}

func TestOriginSSHCertificateAuthorityDeleteProtection(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	stored := mock.add("acme", sampleSSHCAKey, "ca")

	ctx := context.Background()
	res := &originSSHCertificateAuthorityResource{client: mock.client()}
	protected := sampleSSHCAModel()
	protected.ID = types.StringValue(stored.ID)
	protected.DeletionProtection = types.BoolValue(true)
	resp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: sshCAState(t, res, protected)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "deletion_protection") {
		t.Fatalf("diagnostics = %v, want deletion_protection to block delete", resp.Diagnostics)
	}
	if mock.count("DELETE /namespaces/acme/ssh-certificate-authorities/"+stored.ID) != 0 {
		t.Fatal("delete request was sent while protected")
	}

	nullFlag := protected
	nullFlag.DeletionProtection = types.BoolNull()
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: sshCAState(t, res, nullFlag)}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a null deletion_protection must be treated as protected")
	}

	unprotected := protected
	unprotected.DeletionProtection = types.BoolValue(false)
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: sshCAState(t, res, unprotected)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unprotected delete: %v", resp.Diagnostics)
	}
	if mock.count("DELETE /namespaces/acme/ssh-certificate-authorities/"+stored.ID) != 1 {
		t.Fatal("expected the delete request when deletion_protection is false")
	}
}

func TestOriginSSHCertificateAuthorityDestroyPlanRespectsDeletionProtection(t *testing.T) {
	ctx := context.Background()
	res := &originSSHCertificateAuthorityResource{}
	sch := sshCASchema(t, res)
	nullPlan := tfsdk.Plan{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}

	protected := sampleSSHCAModel()
	protected.DeletionProtection = types.BoolValue(true)
	resp := &resource.ModifyPlanResponse{Plan: nullPlan}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: nullPlan, State: sshCAState(t, res, protected)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "deletion_protection") {
		t.Fatalf("diagnostics = %v, want the destroy plan refused while protected", resp.Diagnostics)
	}

	unprotected := protected
	unprotected.DeletionProtection = types.BoolValue(false)
	resp = &resource.ModifyPlanResponse{Plan: nullPlan}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: nullPlan, State: sshCAState(t, res, unprotected)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unprotected destroy plan: %v", resp.Diagnostics)
	}
}

func TestOriginSSHCertificateAuthorityReplacementRespectsDeletionProtection(t *testing.T) {
	ctx := context.Background()
	res := &originSSHCertificateAuthorityResource{}
	sch := sshCASchema(t, res)
	protected := sampleSSHCAModel()
	protected.DeletionProtection = types.BoolValue(true)

	modify := func(state, plan originSSHCertificateAuthorityModel) *resource.ModifyPlanResponse {
		planValue := tfsdk.Plan{Schema: sch}
		if diags := planValue.Set(ctx, &plan); diags.HasError() {
			t.Fatal(diags)
		}
		resp := &resource.ModifyPlanResponse{Plan: planValue}
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, Config: sshCAConfig(t, sch, plan), State: sshCAState(t, res, state)}, resp)
		return resp
	}

	rotated := protected
	rotated.PublicKey = types.StringValue(secondSSHCAKey)
	if resp := modify(protected, rotated); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "deletion_protection") {
		t.Fatalf("diagnostics = %v, want the key rotation refused while protected", resp.Diagnostics)
	}
	// The flag in state is what counts: turning it off in the same plan as the rotation is not enough.
	rotated.DeletionProtection = types.BoolValue(false)
	if resp := modify(protected, rotated); !resp.Diagnostics.HasError() {
		t.Fatal("rotation with deletion_protection = false only in the plan must be refused")
	}
	moved := protected
	moved.Namespace, moved.Owner = types.StringValue("other"), types.StringValue("other")
	if resp := modify(protected, moved); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "changing namespace") {
		t.Fatalf("diagnostics = %v, want the namespace change refused while protected", resp.Diagnostics)
	}
	// Unknown values are planned as a replace by the attribute modifiers, so they are refused too.
	unknownKey := protected
	unknownKey.PublicKey = types.StringUnknown()
	if resp := modify(protected, unknownKey); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "public_key is not known until apply") {
		t.Fatalf("diagnostics = %v, want an unknown key refused while protected", resp.Diagnostics)
	}
	unknownNamespace := protected
	unknownNamespace.Namespace, unknownNamespace.Owner = types.StringUnknown(), types.StringUnknown()
	if resp := modify(protected, unknownNamespace); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "namespace is not known until apply") {
		t.Fatalf("diagnostics = %v, want an unknown namespace refused while protected", resp.Diagnostics)
	}

	commentOnly := protected
	commentOnly.PublicKey = types.StringValue(sampleSSHCAKey + " new-comment")
	if resp := modify(protected, commentOnly); resp.Diagnostics.HasError() {
		t.Fatalf("comment-only change is an in-place update and must plan: %v", resp.Diagnostics)
	}
	unprotect := protected
	unprotect.DeletionProtection = types.BoolValue(false)
	if resp := modify(protected, unprotect); resp.Diagnostics.HasError() {
		t.Fatalf("turning deletion_protection off must plan: %v", resp.Diagnostics)
	}
	rotated.DeletionProtection = types.BoolValue(false)
	if resp := modify(unprotect, rotated); resp.Diagnostics.HasError() {
		t.Fatalf("rotation after deletion_protection = false was applied must plan: %v", resp.Diagnostics)
	}
	unknownKey.DeletionProtection = types.BoolValue(false)
	if resp := modify(unprotect, unknownKey); resp.Diagnostics.HasError() {
		t.Fatalf("unknown key after deletion_protection = false was applied must plan: %v", resp.Diagnostics)
	}
}

func TestOriginSSHCertificateAuthorityModifyPlanAlignsNamespaceAlias(t *testing.T) {
	ctx := context.Background()
	res := &originSSHCertificateAuthorityResource{}
	sch := sshCASchema(t, res)
	modify := func(state *originSSHCertificateAuthorityModel, config, plan originSSHCertificateAuthorityModel) (originSSHCertificateAuthorityModel, *resource.ModifyPlanResponse) {
		t.Helper()
		planValue := tfsdk.Plan{Schema: sch}
		if diags := planValue.Set(ctx, &plan); diags.HasError() {
			t.Fatal(diags)
		}
		prior := emptyState(ctx, sch)
		if state != nil {
			prior = sshCAState(t, res, *state)
		}
		resp := &resource.ModifyPlanResponse{Plan: planValue}
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, Config: sshCAConfig(t, sch, config), State: prior}, resp)
		var got originSSHCertificateAuthorityModel
		if diags := resp.Plan.Get(ctx, &got); diags.HasError() {
			t.Fatal(diags)
		}
		return got, resp
	}

	namespaceOnly := sampleSSHCAModel()
	namespaceOnly.Owner = types.StringNull()
	planned := namespaceOnly
	planned.Owner = types.StringUnknown()
	got, resp := modify(nil, namespaceOnly, planned)
	if resp.Diagnostics.HasError() || got.Namespace.ValueString() != "acme" || got.Owner.ValueString() != "acme" {
		t.Fatalf("namespace-only create plan = %#v, %v", got, resp.Diagnostics)
	}

	ownerOnly := sampleSSHCAModel()
	ownerOnly.Namespace = types.StringNull()
	planned = ownerOnly
	planned.Namespace = types.StringUnknown()
	got, resp = modify(nil, ownerOnly, planned)
	if resp.Diagnostics.HasError() || got.Namespace.ValueString() != "acme" || got.Owner.ValueString() != "acme" {
		t.Fatalf("owner-only create plan = %#v, %v", got, resp.Diagnostics)
	}

	upgraded := sampleSSHCAModel()
	upgraded.DeletionProtection = types.BoolValue(true)
	config := upgraded
	config.Owner = types.StringNull()
	got, resp = modify(&upgraded, config, upgraded)
	if resp.Diagnostics.HasError() || got != upgraded {
		t.Fatalf("switching config from owner to namespace must not change the plan: %#v, %v", got, resp.Diagnostics)
	}

	movedByOwner := upgraded
	movedByOwner.Owner = types.StringValue("other")
	config = movedByOwner
	config.Namespace = types.StringNull()
	got, resp = modify(&upgraded, config, movedByOwner)
	if got.Namespace.ValueString() != "other" || !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "changing namespace") {
		t.Fatalf("plan = %#v, diagnostics = %v, want the deprecated owner change planned as a protected namespace change", got, resp.Diagnostics)
	}
}

func TestOriginSSHCertificateStateUpgradeFromV0(t *testing.T) {
	ctx := context.Background()
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	upgrade := func(typeName string, sch schema.Schema, prior map[string]any, target any) {
		t.Helper()
		raw, err := json.Marshal(prior)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
			TypeName: typeName,
			Version:  0,
			RawState: &tfprotov6.RawState{JSON: raw},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, diag := range resp.Diagnostics {
			if diag.Severity == tfprotov6.DiagnosticSeverityError {
				t.Fatalf("%s upgrade: %s: %s", typeName, diag.Summary, diag.Detail)
			}
		}
		value, err := resp.UpgradedState.Unmarshal(sch.Type().TerraformType(ctx))
		if err != nil {
			t.Fatal(err)
		}
		if diags := (tfsdk.State{Schema: sch, Raw: value}).Get(ctx, target); diags.HasError() {
			t.Fatal(diags)
		}
	}

	var authority originSSHCertificateAuthorityModel
	upgrade("cursor_origin_ssh_certificate_authority", sshCASchema(t, &originSSHCertificateAuthorityResource{}), map[string]any{
		"id":                  sampleSSHCAID,
		"owner":               "acme",
		"name":                "Acme production CA",
		"public_key":          sampleSSHCAKey,
		"key_type":            "ssh-ed25519",
		"fingerprint":         sampleSSHCAFingerprint,
		"created_at":          "2026-08-02T14:45:00Z",
		"deletion_protection": true,
	}, &authority)
	want := sampleSSHCAModel()
	want.DeletionProtection = types.BoolValue(true)
	if authority != want {
		t.Fatalf("upgraded authority = %#v, want %#v", authority, want)
	}

	var requirement originSSHCertificateRequirementModel
	upgrade("cursor_origin_ssh_certificate_requirement", sshRequirementSchema(t, &originSSHCertificateRequirementResource{}), map[string]any{
		"id":                   "acme",
		"owner":                "acme",
		"require_certificates": true,
		"deletion_protection":  true,
	}, &requirement)
	wantRequirement := originSSHCertificateRequirementModel{ID: types.StringValue("acme"), Namespace: types.StringValue("acme"), Owner: types.StringValue("acme"), RequireCertificates: types.BoolValue(true), DeletionProtection: types.BoolValue(true)}
	if requirement != wantRequirement {
		t.Fatalf("upgraded requirement = %#v, want %#v", requirement, wantRequirement)
	}
}

type mockClientProvider struct {
	provider.Provider
	client *apiClient
}

func (p mockClientProvider) Configure(_ context.Context, _ provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	resp.ResourceData = p.client
}

func TestOriginSSHCertificateAliasesKnownAfterApplyWhenSlugIsUnknownAtPlan(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	ctx := context.Background()
	server, err := providerserver.NewProtocol6WithError(mockClientProvider{Provider: New("test")(), client: mock.client()})()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	object := func(typ tftypes.Type, set map[string]tftypes.Value) tftypes.Value {
		values := map[string]tftypes.Value{}
		for name, attr := range typ.(tftypes.Object).AttributeTypes {
			values[name] = tftypes.NewValue(attr, nil)
		}
		maps.Copy(values, set)
		return tftypes.NewValue(typ, values)
	}
	encode := func(typ tftypes.Type, value tftypes.Value) *tfprotov6.DynamicValue {
		t.Helper()
		encoded, err := tfprotov6.NewDynamicValue(typ, value)
		if err != nil {
			t.Fatal(err)
		}
		return &encoded
	}
	requireNoErrors := func(step string, diags []*tfprotov6.Diagnostic) {
		t.Helper()
		for _, diag := range diags {
			if diag.Severity == tfprotov6.DiagnosticSeverityError {
				t.Fatalf("%s: %s: %s", step, diag.Summary, diag.Detail)
			}
		}
	}
	aliases := func(typ tftypes.Type, state *tfprotov6.DynamicValue) (tftypes.Value, tftypes.Value) {
		t.Helper()
		value, err := state.Unmarshal(typ)
		if err != nil {
			t.Fatal(err)
		}
		var attrs map[string]tftypes.Value
		if err := value.As(&attrs); err != nil {
			t.Fatal(err)
		}
		return attrs["namespace"], attrs["owner"]
	}

	providerType := schemas.Provider.ValueType()
	configured, err := server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: encode(providerType, object(providerType, nil))})
	if err != nil {
		t.Fatal(err)
	}
	requireNoErrors("configure", configured.Diagnostics)

	for _, tc := range []struct{ alias, slug, key string }{
		{"namespace", "acme", sampleSSHCAKey},
		{"owner", "globex", secondSSHCAKey},
	} {
		for _, r := range []struct {
			typeName, collection string
			config               map[string]tftypes.Value
		}{
			{"cursor_origin_ssh_certificate_authority", "ssh-certificate-authorities", map[string]tftypes.Value{
				"name":       tftypes.NewValue(tftypes.String, "Acme production CA"),
				"public_key": tftypes.NewValue(tftypes.String, tc.key),
			}},
			{"cursor_origin_ssh_certificate_requirement", "ssh-certificate-authorities:setRequirement", map[string]tftypes.Value{
				"require_certificates": tftypes.NewValue(tftypes.Bool, true),
			}},
		} {
			typ := schemas.ResourceSchemas[r.typeName].ValueType()
			prior := encode(typ, tftypes.NewValue(typ, nil))
			plan := func(slug any) (*tfprotov6.DynamicValue, *tfprotov6.PlanResourceChangeResponse) {
				t.Helper()
				set := maps.Clone(r.config)
				set[tc.alias] = tftypes.NewValue(tftypes.String, slug)
				config := encode(typ, object(typ, set))
				resp, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{TypeName: r.typeName, PriorState: prior, ProposedNewState: config, Config: config})
				if err != nil {
					t.Fatal(err)
				}
				requireNoErrors(r.typeName+" plan", resp.Diagnostics)
				return config, resp
			}

			_, unknown := plan(tftypes.UnknownValue)
			if namespace, owner := aliases(typ, unknown.PlannedState); namespace.IsKnown() || owner.IsKnown() {
				t.Fatalf("%s with %s unknown at plan: namespace = %s, owner = %s, want both unknown", r.typeName, tc.alias, namespace, owner)
			}
			config, planned := plan(tc.slug)
			applied, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{TypeName: r.typeName, PriorState: prior, PlannedState: planned.PlannedState, PlannedPrivate: planned.PlannedPrivate, Config: config})
			if err != nil {
				t.Fatal(err)
			}
			requireNoErrors(r.typeName+" apply", applied.Diagnostics)
			want := tftypes.NewValue(tftypes.String, tc.slug)
			route := "POST /namespaces/" + tc.slug + "/" + r.collection
			if namespace, owner := aliases(typ, applied.NewState); !namespace.Equal(want) || !owner.Equal(want) || mock.count(route) != 1 {
				t.Fatalf("%s with %s known only at apply: namespace = %s, owner = %s, %s calls = %d", r.typeName, tc.alias, namespace, owner, route, mock.count(route))
			}
		}
	}
}

func TestOriginSSHCertificateAuthorityDeletePreconditionThenNotFound(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	stored := mock.add("acme", sampleSSHCAKey, "ca")
	mock.setRequire("acme", true)

	ctx := context.Background()
	res := &originSSHCertificateAuthorityResource{client: mock.client()}
	model := sampleSSHCAModel()
	model.ID = types.StringValue(stored.ID)
	state := sshCAState(t, res, model)

	resp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: state}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "last certificate authority") {
		t.Fatalf("diagnostics = %v, want the precondition surfaced", resp.Diagnostics)
	}

	mock.setRequire("acme", false)
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete diagnostics: %v", resp.Diagnostics)
	}
	if mock.count("DELETE /namespaces/acme/ssh-certificate-authorities/"+stored.ID) != 2 {
		t.Fatalf("delete calls = %d", mock.count("DELETE /namespaces/acme/ssh-certificate-authorities/"+stored.ID))
	}

	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("deleting a removed authority must succeed: %v", resp.Diagnostics)
	}
}

func TestOriginSSHCertificateAuthorityImportState(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	stored := mock.add("acme", sampleSSHCAKey, "ca")

	ctx := context.Background()
	res := &originSSHCertificateAuthorityResource{client: mock.client()}
	sch := sshCASchema(t, res)
	importCA := func(id string) (originSSHCertificateAuthorityModel, *resource.ImportStateResponse) {
		resp := &resource.ImportStateResponse{State: emptyState(ctx, sch)}
		res.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
		var model originSSHCertificateAuthorityModel
		if !resp.Diagnostics.HasError() {
			if diags := resp.State.Get(ctx, &model); diags.HasError() {
				t.Fatal(diags)
			}
		}
		return model, resp
	}

	got, resp := importCA("acme:" + stored.ID)
	if resp.Diagnostics.HasError() || got.Namespace.ValueString() != "acme" || got.Owner.ValueString() != "acme" || got.ID.ValueString() != stored.ID || !got.PublicKey.IsNull() || !got.DeletionProtection.ValueBool() {
		t.Fatalf("import by id = %#v, %v", got, resp.Diagnostics)
	}
	got, resp = importCA("acme:" + sampleSSHCAFingerprint)
	if resp.Diagnostics.HasError() || got.ID.ValueString() != stored.ID {
		t.Fatalf("import by fingerprint = %#v, %v", got, resp.Diagnostics)
	}
	if mock.count("GET /namespaces/acme/ssh-certificate-authorities") != 1 {
		t.Fatal("import by id must not list; import by fingerprint lists once")
	}
	if _, resp = importCA("acme:" + secondSSHCAFingerprint); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "no SSH certificate authority with fingerprint") {
		t.Fatalf("diagnostics = %v, want unknown fingerprint", resp.Diagnostics)
	}
	for _, id := range []string{"acme", "acme:", ":nsca_01", "acme/rocket:nsca_01", " acme:nsca_01"} {
		if _, resp = importCA(id); !resp.Diagnostics.HasError() {
			t.Fatalf("import %q must be rejected", id)
		}
	}
}

func TestValidateOriginSSHCertificateAuthority(t *testing.T) {
	valid := sampleSSHCAModel()
	if err := validateOriginSSHCertificateAuthority(valid); err != nil {
		t.Fatalf("valid model rejected: %v", err)
	}
	unknown := valid
	unknown.Namespace, unknown.Owner, unknown.Name, unknown.PublicKey = types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	if err := validateOriginSSHCertificateAuthority(unknown); err != nil {
		t.Fatalf("unknown values rejected: %v", err)
	}
	namespaceOnly := valid
	namespaceOnly.Owner = types.StringNull()
	if err := validateOriginSSHCertificateAuthority(namespaceOnly); err != nil {
		t.Fatalf("namespace without owner rejected: %v", err)
	}
	ownerOnly := valid
	ownerOnly.Namespace = types.StringNull()
	if err := validateOriginSSHCertificateAuthority(ownerOnly); err != nil {
		t.Fatalf("deprecated owner without namespace rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*originSSHCertificateAuthorityModel)
		want   string
	}{
		{"no namespace", func(m *originSSHCertificateAuthorityModel) {
			m.Namespace, m.Owner = types.StringNull(), types.StringNull()
		}, "namespace is required"},
		{"namespace with slash", func(m *originSSHCertificateAuthorityModel) { m.Namespace = types.StringValue("acme/rocket") }, "namespace must not contain"},
		{"owner with slash", func(m *originSSHCertificateAuthorityModel) {
			m.Namespace, m.Owner = types.StringNull(), types.StringValue("acme/rocket")
		}, "owner must not contain"},
		{"namespace and owner differ", func(m *originSSHCertificateAuthorityModel) { m.Owner = types.StringValue("other") }, "must match"},
		{"empty name", func(m *originSSHCertificateAuthorityModel) { m.Name = types.StringValue("") }, "name is required"},
		{"padded name", func(m *originSSHCertificateAuthorityModel) { m.Name = types.StringValue(" ca") }, "name must not have"},
		{"long name", func(m *originSSHCertificateAuthorityModel) { m.Name = types.StringValue(strings.Repeat("a", 256)) }, "at most 255"},
		{"null key", func(m *originSSHCertificateAuthorityModel) { m.PublicKey = types.StringNull() }, "public_key is required"},
		{"one field", func(m *originSSHCertificateAuthorityModel) { m.PublicKey = types.StringValue("ssh-ed25519") }, "authorized_keys line"},
		{"two lines", func(m *originSSHCertificateAuthorityModel) {
			m.PublicKey = types.StringValue(sampleSSHCAKey + "\n" + secondSSHCAKey)
		}, "authorized_keys line"},
		{"certificate", func(m *originSSHCertificateAuthorityModel) {
			m.PublicKey = types.StringValue("ssh-ed25519-cert-v01@openssh.com AAAA cert")
		}, "is an SSH certificate"},
	}
	for _, tc := range cases {
		model := sampleSSHCAModel()
		tc.mutate(&model)
		err := validateOriginSSHCertificateAuthority(model)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want %q", tc.name, err, tc.want)
		}
	}
}

// The sample is unprotected so the create, update, and delete tests exercise the API path;
// the deletion_protection tests set the flag explicitly.
func sampleSSHCAModel() originSSHCertificateAuthorityModel {
	return originSSHCertificateAuthorityModel{
		ID:                 types.StringValue(sampleSSHCAID),
		Namespace:          types.StringValue("acme"),
		Owner:              types.StringValue("acme"),
		Name:               types.StringValue("Acme production CA"),
		PublicKey:          types.StringValue(sampleSSHCAKey),
		KeyType:            types.StringValue("ssh-ed25519"),
		Fingerprint:        types.StringValue(sampleSSHCAFingerprint),
		CreatedAt:          types.StringValue("2026-08-02T14:45:00Z"),
		DeletionProtection: types.BoolValue(false),
	}
}

func sshCASchema(t *testing.T, res *originSSHCertificateAuthorityResource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	res.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func sshCAConfig(t *testing.T, sch schema.Schema, model originSSHCertificateAuthorityModel) tfsdk.Config {
	t.Helper()
	model.ID, model.KeyType, model.Fingerprint, model.CreatedAt = types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull()
	value := tfsdk.Plan{Schema: sch}
	if diags := value.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return tfsdk.Config{Schema: sch, Raw: value.Raw}
}

func sshCAState(t *testing.T, res *originSSHCertificateAuthorityResource, model originSSHCertificateAuthorityModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: sshCASchema(t, res)}
	if diags := state.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return state
}

func createSSHCA(t *testing.T, res *originSSHCertificateAuthorityResource, plan originSSHCertificateAuthorityModel) originSSHCertificateAuthorityModel {
	t.Helper()
	ctx := context.Background()
	planValue := tfsdk.Plan{Schema: sshCASchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: planValue.Schema}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create diagnostics: %v", resp.Diagnostics)
	}
	var got originSSHCertificateAuthorityModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	return got
}

// Returns nil when Read removed the resource from state.
func readSSHCA(t *testing.T, res *originSSHCertificateAuthorityResource, model originSSHCertificateAuthorityModel) *originSSHCertificateAuthorityModel {
	t.Helper()
	ctx := context.Background()
	state := sshCAState(t, res, model)
	resp := &resource.ReadResponse{State: state}
	res.Read(ctx, resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		return nil
	}
	var got originSSHCertificateAuthorityModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	return &got
}
