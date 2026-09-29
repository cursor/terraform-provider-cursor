package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestOriginSSHCertificateRequirementCreateUpdateDelete(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	mock.add("acme", sampleSSHCAKey, "ca")

	ctx := context.Background()
	res := &originSSHCertificateRequirementResource{client: mock.client()}
	sch := sshRequirementSchema(t, res)
	plan := originSSHCertificateRequirementModel{ID: types.StringUnknown(), Namespace: types.StringValue("acme"), Owner: types.StringValue("acme"), RequireCertificates: types.BoolValue(true), DeletionProtection: types.BoolValue(false)}
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create diagnostics: %v", createResp.Diagnostics)
	}
	var got originSSHCertificateRequirementModel
	if diags := createResp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if got.ID.ValueString() != "acme" || !got.RequireCertificates.ValueBool() || got.DeletionProtection.ValueBool() {
		t.Fatalf("state = %#v", got)
	}
	if body := mock.lastBody("POST /namespaces/acme/ssh-certificate-authorities:setRequirement"); body != `{"requireCertificates":true}` {
		t.Fatalf("create body = %s", body)
	}

	plan.RequireCertificates = types.BoolValue(false)
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
	res.Update(ctx, resource.UpdateRequest{Plan: planValue, State: createResp.State}, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update diagnostics: %v", updateResp.Diagnostics)
	}
	if body := mock.lastBody("POST /namespaces/acme/ssh-certificate-authorities:setRequirement"); body != `{"requireCertificates":false}` {
		t.Fatalf("update body = %s", body)
	}

	mock.setRequire("acme", true)
	deleteResp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: createResp.State}, deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete diagnostics: %v", deleteResp.Diagnostics)
	}
	if body := mock.lastBody("POST /namespaces/acme/ssh-certificate-authorities:setRequirement"); body != `{"requireCertificates":false}` {
		t.Fatalf("delete body = %s", body)
	}
	if mock.count("POST /namespaces/acme/ssh-certificate-authorities:setRequirement") != 3 {
		t.Fatalf("setRequirement calls = %d", mock.count("POST /namespaces/acme/ssh-certificate-authorities:setRequirement"))
	}
}

func TestOriginSSHCertificateRequirementCreateWithoutAuthorityFails(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originSSHCertificateRequirementResource{client: mock.client()}
	sch := sshRequirementSchema(t, res)
	plan := originSSHCertificateRequirementModel{ID: types.StringUnknown(), Namespace: types.StringValue("acme"), Owner: types.StringValue("acme"), RequireCertificates: types.BoolValue(true)}
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "before requiring certificates") {
		t.Fatalf("diagnostics = %v, want the precondition surfaced", resp.Diagnostics)
	}
}

func TestOriginSSHCertificateRequirementRead(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	mock.add("acme", sampleSSHCAKey, "ca")
	mock.setRequire("acme", true)

	ctx := context.Background()
	res := &originSSHCertificateRequirementResource{client: mock.client()}
	state := sshRequirementState(t, res, originSSHCertificateRequirementModel{ID: types.StringValue("acme"), Namespace: types.StringValue("acme"), Owner: types.StringValue("acme"), RequireCertificates: types.BoolNull()})
	resp := &resource.ReadResponse{State: state}
	res.Read(ctx, resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	var got originSSHCertificateRequirementModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if !got.RequireCertificates.ValueBool() || got.ID.ValueString() != "acme" {
		t.Fatalf("state = %#v", got)
	}
	if !got.DeletionProtection.ValueBool() {
		t.Fatal("a null deletion_protection must read back as protected")
	}
}

func TestOriginSSHCertificateRequirementReadErrorKeepsState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, originStatusError{Code: 5, Message: "missing"})
	}))
	defer server.Close()

	res := &originSSHCertificateRequirementResource{client: testOriginClient(server)}
	state := sshRequirementState(t, res, originSSHCertificateRequirementModel{ID: types.StringValue("acme"), Namespace: types.StringValue("acme"), Owner: types.StringValue("acme"), RequireCertificates: types.BoolValue(true), DeletionProtection: types.BoolValue(false)})
	resp := &resource.ReadResponse{State: state}
	res.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected read error when the namespace cannot be listed")
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("read removed state on 404")
	}

	deleteResp := &resource.DeleteResponse{}
	res.Delete(context.Background(), resource.DeleteRequest{State: state}, deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete of a missing namespace must succeed: %v", deleteResp.Diagnostics)
	}
}

func TestOriginSSHCertificateRequirementImportAndValidate(t *testing.T) {
	ctx := context.Background()
	res := &originSSHCertificateRequirementResource{}
	sch := sshRequirementSchema(t, res)

	resp := &resource.ImportStateResponse{State: emptyState(ctx, sch)}
	res.ImportState(ctx, resource.ImportStateRequest{ID: "acme"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import diagnostics: %v", resp.Diagnostics)
	}
	var got originSSHCertificateRequirementModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if got.Namespace.ValueString() != "acme" || got.Owner.ValueString() != "acme" || got.ID.ValueString() != "acme" || !got.RequireCertificates.IsNull() || !got.DeletionProtection.ValueBool() {
		t.Fatalf("imported = %#v", got)
	}
	for _, id := range []string{"", "acme/rocket", "acme:x", " acme"} {
		resp = &resource.ImportStateResponse{State: emptyState(ctx, sch)}
		res.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatalf("import %q must be rejected", id)
		}
	}

	validate := func(namespace, owner types.String) error {
		model := originSSHCertificateRequirementModel{Namespace: namespace, Owner: owner, RequireCertificates: types.BoolValue(true)}
		validateResp := &resource.ValidateConfigResponse{}
		res.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: sshRequirementConfig(t, sch, model)}, validateResp)
		if validateResp.Diagnostics.HasError() {
			return errors.New(validateResp.Diagnostics.Errors()[0].Detail())
		}
		return nil
	}
	for name, tc := range map[string]struct {
		namespace, owner types.String
		want             string
	}{
		"namespace":            {types.StringValue("acme"), types.StringNull(), ""},
		"deprecated owner":     {types.StringNull(), types.StringValue("acme"), ""},
		"both matching":        {types.StringValue("acme"), types.StringValue("acme"), ""},
		"unknown namespace":    {types.StringUnknown(), types.StringNull(), ""},
		"neither":              {types.StringNull(), types.StringNull(), "namespace is required"},
		"bad namespace":        {types.StringValue("acme:bad"), types.StringNull(), "namespace must not contain"},
		"bad deprecated owner": {types.StringNull(), types.StringValue("acme:bad"), "owner must not contain"},
		"mismatch":             {types.StringValue("acme"), types.StringValue("other"), "must match"},
	} {
		err := validate(tc.namespace, tc.owner)
		if tc.want == "" && err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: error = %v, want %q", name, err, tc.want)
		}
	}
}

func TestOriginSSHCertificateRequirementModifyPlanAlignsNamespaceAlias(t *testing.T) {
	ctx := context.Background()
	res := &originSSHCertificateRequirementResource{}
	sch := sshRequirementSchema(t, res)
	for name, tc := range map[string]struct {
		config, plan originSSHCertificateRequirementModel
	}{
		"namespace": {
			config: originSSHCertificateRequirementModel{Namespace: types.StringValue("acme"), RequireCertificates: types.BoolValue(true)},
			plan:   originSSHCertificateRequirementModel{ID: types.StringUnknown(), Namespace: types.StringValue("acme"), Owner: types.StringUnknown(), RequireCertificates: types.BoolValue(true), DeletionProtection: types.BoolValue(true)},
		},
		"deprecated owner": {
			config: originSSHCertificateRequirementModel{Owner: types.StringValue("acme"), RequireCertificates: types.BoolValue(true)},
			plan:   originSSHCertificateRequirementModel{ID: types.StringUnknown(), Namespace: types.StringUnknown(), Owner: types.StringValue("acme"), RequireCertificates: types.BoolValue(true), DeletionProtection: types.BoolValue(true)},
		},
	} {
		planValue := tfsdk.Plan{Schema: sch}
		if diags := planValue.Set(ctx, &tc.plan); diags.HasError() {
			t.Fatal(diags)
		}
		resp := &resource.ModifyPlanResponse{Plan: planValue}
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, Config: sshRequirementConfig(t, sch, tc.config), State: emptyState(ctx, sch)}, resp)
		var got originSSHCertificateRequirementModel
		if diags := resp.Plan.Get(ctx, &got); diags.HasError() {
			t.Fatal(diags)
		}
		if resp.Diagnostics.HasError() || got.Namespace.ValueString() != "acme" || got.Owner.ValueString() != "acme" {
			t.Fatalf("%s: plan = %#v, %v", name, got, resp.Diagnostics)
		}
	}
}

func TestOriginSSHCertificateRequirementUpdateOfDeletionProtectionSkipsAPI(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originSSHCertificateRequirementResource{client: mock.client()}
	sch := sshRequirementSchema(t, res)
	prior := originSSHCertificateRequirementModel{ID: types.StringValue("acme"), Namespace: types.StringValue("acme"), Owner: types.StringValue("acme"), RequireCertificates: types.BoolValue(true), DeletionProtection: types.BoolValue(true)}
	plan := prior
	plan.DeletionProtection = types.BoolValue(false)
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
	res.Update(ctx, resource.UpdateRequest{Plan: planValue, State: sshRequirementState(t, res, prior)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update diagnostics: %v", resp.Diagnostics)
	}
	var got originSSHCertificateRequirementModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if got.DeletionProtection.ValueBool() || !got.RequireCertificates.ValueBool() || got.ID.ValueString() != "acme" {
		t.Fatalf("state = %#v", got)
	}
	if mock.count("POST /namespaces/acme/ssh-certificate-authorities:setRequirement") != 0 {
		t.Fatal("changing only deletion_protection must not call setRequirement")
	}
}

func TestOriginSSHCertificateRequirementDeleteProtection(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	mock.add("acme", sampleSSHCAKey, "ca")
	mock.setRequire("acme", true)

	ctx := context.Background()
	res := &originSSHCertificateRequirementResource{client: mock.client()}
	protected := originSSHCertificateRequirementModel{ID: types.StringValue("acme"), Namespace: types.StringValue("acme"), Owner: types.StringValue("acme"), RequireCertificates: types.BoolValue(true), DeletionProtection: types.BoolValue(true)}
	resp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: sshRequirementState(t, res, protected)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "deletion_protection") {
		t.Fatalf("diagnostics = %v, want deletion_protection to block delete", resp.Diagnostics)
	}
	nullFlag := protected
	nullFlag.DeletionProtection = types.BoolNull()
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: sshRequirementState(t, res, nullFlag)}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a null deletion_protection must be treated as protected")
	}
	if mock.count("POST /namespaces/acme/ssh-certificate-authorities:setRequirement") != 0 {
		t.Fatal("the flag was cleared while protected")
	}

	unprotected := protected
	unprotected.DeletionProtection = types.BoolValue(false)
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: sshRequirementState(t, res, unprotected)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unprotected delete: %v", resp.Diagnostics)
	}
	if body := mock.lastBody("POST /namespaces/acme/ssh-certificate-authorities:setRequirement"); body != `{"requireCertificates":false}` {
		t.Fatalf("delete body = %s, want the flag cleared once deletion_protection is false", body)
	}
}

func TestOriginSSHCertificateRequirementModifyPlanRespectsDeletionProtection(t *testing.T) {
	ctx := context.Background()
	res := &originSSHCertificateRequirementResource{}
	sch := sshRequirementSchema(t, res)
	nullPlan := tfsdk.Plan{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
	protected := originSSHCertificateRequirementModel{ID: types.StringValue("acme"), Namespace: types.StringValue("acme"), Owner: types.StringValue("acme"), RequireCertificates: types.BoolValue(true), DeletionProtection: types.BoolValue(true)}

	resp := &resource.ModifyPlanResponse{Plan: nullPlan}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: nullPlan, State: sshRequirementState(t, res, protected)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "deletion_protection") {
		t.Fatalf("diagnostics = %v, want the destroy plan refused while protected", resp.Diagnostics)
	}

	modify := func(state, plan originSSHCertificateRequirementModel) *resource.ModifyPlanResponse {
		planValue := tfsdk.Plan{Schema: sch}
		if diags := planValue.Set(ctx, &plan); diags.HasError() {
			t.Fatal(diags)
		}
		resp := &resource.ModifyPlanResponse{Plan: planValue}
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, Config: sshRequirementConfig(t, sch, plan), State: sshRequirementState(t, res, state)}, resp)
		return resp
	}
	moved := protected
	moved.Namespace, moved.Owner = types.StringValue("other"), types.StringValue("other")
	if resp := modify(protected, moved); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "changing namespace") {
		t.Fatalf("diagnostics = %v, want the namespace change refused while protected", resp.Diagnostics)
	}
	unknownNamespace := protected
	unknownNamespace.Namespace, unknownNamespace.Owner = types.StringUnknown(), types.StringUnknown()
	if resp := modify(protected, unknownNamespace); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "namespace is not known until apply") {
		t.Fatalf("diagnostics = %v, want an unknown namespace refused while protected", resp.Diagnostics)
	}
	relaxed := protected
	relaxed.RequireCertificates = types.BoolValue(false)
	if resp := modify(protected, relaxed); resp.Diagnostics.HasError() {
		t.Fatalf("require_certificates is an in-place update and must plan: %v", resp.Diagnostics)
	}

	unprotected := protected
	unprotected.DeletionProtection = types.BoolValue(false)
	resp = &resource.ModifyPlanResponse{Plan: nullPlan}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: nullPlan, State: sshRequirementState(t, res, unprotected)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unprotected destroy plan: %v", resp.Diagnostics)
	}
	moved.DeletionProtection = types.BoolValue(false)
	if resp := modify(unprotected, moved); resp.Diagnostics.HasError() {
		t.Fatalf("namespace change after deletion_protection = false was applied must plan: %v", resp.Diagnostics)
	}
	unknownNamespace.DeletionProtection = types.BoolValue(false)
	if resp := modify(unprotected, unknownNamespace); resp.Diagnostics.HasError() {
		t.Fatalf("unknown namespace after deletion_protection = false was applied must plan: %v", resp.Diagnostics)
	}
}

func sshRequirementSchema(t *testing.T, res *originSSHCertificateRequirementResource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	res.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func sshRequirementConfig(t *testing.T, sch schema.Schema, model originSSHCertificateRequirementModel) tfsdk.Config {
	t.Helper()
	model.ID = types.StringNull()
	value := tfsdk.Plan{Schema: sch}
	if diags := value.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return tfsdk.Config{Schema: sch, Raw: value.Raw}
}

func sshRequirementState(t *testing.T, res *originSSHCertificateRequirementResource, model originSSHCertificateRequirementModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: sshRequirementSchema(t, res)}
	if diags := state.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return state
}
