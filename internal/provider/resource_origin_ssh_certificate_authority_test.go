package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
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
	if body := mock.lastBody("POST /owners/acme/ssh-certificate-authorities"); body != `{"publicKey":"`+sampleSSHCAKey+` acme-ssh-ca","name":"Acme production CA"}` {
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
		t.Fatal("expected read error when the owner cannot be listed")
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
	if mock.count("POST /owners/acme/ssh-certificate-authorities") != 0 {
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
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: sshCAState(t, res, state)}, resp)
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
	moved.Owner = types.StringValue("other")
	if resp := modify(moved); resp.Diagnostics.HasError() {
		t.Fatalf("rename together with an owner change is a replace and must plan: %v", resp.Diagnostics)
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
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: create.Plan, State: emptyState(ctx, sch)}, create)
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
	if len(authority.Attributes["owner"].(schema.StringAttribute).PlanModifiers) != 1 {
		t.Fatal("authority.owner must require replace")
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
	if mock.count("DELETE /owners/acme/ssh-certificate-authorities/"+stored.ID) != 0 {
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
	if mock.count("DELETE /owners/acme/ssh-certificate-authorities/"+stored.ID) != 1 {
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
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: sshCAState(t, res, state)}, resp)
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
	moved.Owner = types.StringValue("other")
	if resp := modify(protected, moved); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "changing owner") {
		t.Fatalf("diagnostics = %v, want the owner change refused while protected", resp.Diagnostics)
	}
	// Unknown values are planned as a replace by the attribute modifiers, so they are refused too.
	unknownKey := protected
	unknownKey.PublicKey = types.StringUnknown()
	if resp := modify(protected, unknownKey); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "public_key is not known until apply") {
		t.Fatalf("diagnostics = %v, want an unknown key refused while protected", resp.Diagnostics)
	}
	unknownOwner := protected
	unknownOwner.Owner = types.StringUnknown()
	if resp := modify(protected, unknownOwner); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "owner is not known until apply") {
		t.Fatalf("diagnostics = %v, want an unknown owner refused while protected", resp.Diagnostics)
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
	if mock.count("DELETE /owners/acme/ssh-certificate-authorities/"+stored.ID) != 2 {
		t.Fatalf("delete calls = %d", mock.count("DELETE /owners/acme/ssh-certificate-authorities/"+stored.ID))
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
	if resp.Diagnostics.HasError() || got.Owner.ValueString() != "acme" || got.ID.ValueString() != stored.ID || !got.PublicKey.IsNull() || !got.DeletionProtection.ValueBool() {
		t.Fatalf("import by id = %#v, %v", got, resp.Diagnostics)
	}
	got, resp = importCA("acme:" + sampleSSHCAFingerprint)
	if resp.Diagnostics.HasError() || got.ID.ValueString() != stored.ID {
		t.Fatalf("import by fingerprint = %#v, %v", got, resp.Diagnostics)
	}
	if mock.count("GET /owners/acme/ssh-certificate-authorities") != 1 {
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
	unknown.Owner, unknown.Name, unknown.PublicKey = types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	if err := validateOriginSSHCertificateAuthority(unknown); err != nil {
		t.Fatalf("unknown values rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*originSSHCertificateAuthorityModel)
		want   string
	}{
		{"owner with slash", func(m *originSSHCertificateAuthorityModel) { m.Owner = types.StringValue("acme/rocket") }, "owner must not contain"},
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
