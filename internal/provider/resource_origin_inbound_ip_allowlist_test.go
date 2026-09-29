package provider

import (
	"context"
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

func TestOriginInboundIPAllowlistCreateUpdateDelete(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.add("acme", "203.0.113.0/24", "Office", true)

	ctx := context.Background()
	res := &originInboundIPAllowlistResource{client: mock.client()}
	sch := inboundAllowlistSchema(t, res)
	plan := originInboundIPAllowlistModel{ID: types.StringUnknown(), Namespace: types.StringValue("acme"), Enabled: types.BoolValue(true), DeletionProtection: types.BoolValue(false)}
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create diagnostics: %v", createResp.Diagnostics)
	}
	var got originInboundIPAllowlistModel
	if diags := createResp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if got.ID.ValueString() != "acme" || !got.Enabled.ValueBool() || got.DeletionProtection.ValueBool() {
		t.Fatalf("state = %#v", got)
	}
	if body := mock.lastBody("PATCH /namespaces/acme/inbound-ip-allowlist"); body != `{"enabled":true}` || !mock.enabled("acme") {
		t.Fatalf("create body = %s, enabled = %v", body, mock.enabled("acme"))
	}

	plan.Enabled = types.BoolValue(false)
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
	res.Update(ctx, resource.UpdateRequest{Plan: planValue, State: createResp.State}, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update diagnostics: %v", updateResp.Diagnostics)
	}
	if body := mock.lastBody("PATCH /namespaces/acme/inbound-ip-allowlist"); body != `{"enabled":false}` || mock.enabled("acme") {
		t.Fatalf("update body = %s, enabled = %v", body, mock.enabled("acme"))
	}

	mock.setEnabled("acme", true)
	deleteResp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: createResp.State}, deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete diagnostics: %v", deleteResp.Diagnostics)
	}
	if body := mock.lastBody("PATCH /namespaces/acme/inbound-ip-allowlist"); body != `{"enabled":false}` || mock.enabled("acme") {
		t.Fatalf("delete body = %s, enabled = %v", body, mock.enabled("acme"))
	}
	if mock.count("PATCH /namespaces/acme/inbound-ip-allowlist") != 3 {
		t.Fatalf("PATCH calls = %d", mock.count("PATCH /namespaces/acme/inbound-ip-allowlist"))
	}
}

func TestOriginInboundIPAllowlistEnableExcludingCallerFails(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.setCaller("192.0.2.10")
	mock.add("acme", "203.0.113.0/24", "Office", true)

	ctx := context.Background()
	res := &originInboundIPAllowlistResource{client: mock.client()}
	sch := inboundAllowlistSchema(t, res)
	plan := originInboundIPAllowlistModel{ID: types.StringUnknown(), Namespace: types.StringValue("acme"), Enabled: types.BoolValue(true)}
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "exclude your own address") {
		t.Fatalf("diagnostics = %v, want the INVALID_ARGUMENT surfaced", resp.Diagnostics)
	}
	if mock.enabled("acme") {
		t.Fatal("enforcement turned on despite the rejection")
	}
}

func TestOriginInboundIPAllowlistRead(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.add("acme", "203.0.113.0/24", "Office", true)
	mock.setEnabled("acme", true)

	ctx := context.Background()
	res := &originInboundIPAllowlistResource{client: mock.client()}
	state := inboundAllowlistState(t, res, originInboundIPAllowlistModel{ID: types.StringValue("acme"), Namespace: types.StringValue("acme"), Enabled: types.BoolNull()})
	resp := &resource.ReadResponse{State: state}
	res.Read(ctx, resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	var got originInboundIPAllowlistModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if !got.Enabled.ValueBool() || got.ID.ValueString() != "acme" {
		t.Fatalf("state = %#v", got)
	}
	if !got.DeletionProtection.ValueBool() {
		t.Fatal("a null deletion_protection must read back as protected")
	}
}

func TestOriginInboundIPAllowlistReadErrorKeepsState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, originStatusError{Code: 5, Message: "missing"})
	}))
	defer server.Close()

	res := &originInboundIPAllowlistResource{client: testOriginClient(server)}
	state := inboundAllowlistState(t, res, originInboundIPAllowlistModel{ID: types.StringValue("acme"), Namespace: types.StringValue("acme"), Enabled: types.BoolValue(true), DeletionProtection: types.BoolValue(false)})
	resp := &resource.ReadResponse{State: state}
	res.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected read error when the namespace cannot be read")
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

func TestOriginInboundIPAllowlistImportAndValidate(t *testing.T) {
	ctx := context.Background()
	res := &originInboundIPAllowlistResource{}
	sch := inboundAllowlistSchema(t, res)

	resp := &resource.ImportStateResponse{State: emptyState(ctx, sch)}
	res.ImportState(ctx, resource.ImportStateRequest{ID: "acme"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import diagnostics: %v", resp.Diagnostics)
	}
	var got originInboundIPAllowlistModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if got.Namespace.ValueString() != "acme" || got.ID.ValueString() != "acme" || !got.Enabled.IsNull() || !got.DeletionProtection.ValueBool() {
		t.Fatalf("imported = %#v", got)
	}
	for _, id := range []string{"", "acme/rocket", "acme:x", " acme"} {
		resp = &resource.ImportStateResponse{State: emptyState(ctx, sch)}
		res.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatalf("import %q must be rejected", id)
		}
	}

	config := tfsdk.Plan{Schema: sch}
	model := originInboundIPAllowlistModel{ID: types.StringNull(), Namespace: types.StringValue("acme:bad"), Enabled: types.BoolValue(true)}
	if diags := config.Set(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	validateResp := &resource.ValidateConfigResponse{}
	res.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sch, Raw: config.Raw}}, validateResp)
	if !validateResp.Diagnostics.HasError() {
		t.Fatal("expected namespace slug validation error")
	}
}

func TestOriginInboundIPAllowlistUpdateOfDeletionProtectionSkipsAPI(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originInboundIPAllowlistResource{client: mock.client()}
	sch := inboundAllowlistSchema(t, res)
	prior := originInboundIPAllowlistModel{ID: types.StringValue("acme"), Namespace: types.StringValue("acme"), Enabled: types.BoolValue(true), DeletionProtection: types.BoolValue(true)}
	plan := prior
	plan.DeletionProtection = types.BoolValue(false)
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
	res.Update(ctx, resource.UpdateRequest{Plan: planValue, State: inboundAllowlistState(t, res, prior)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update diagnostics: %v", resp.Diagnostics)
	}
	var got originInboundIPAllowlistModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if got.DeletionProtection.ValueBool() || !got.Enabled.ValueBool() || got.ID.ValueString() != "acme" {
		t.Fatalf("state = %#v", got)
	}
	if mock.count("PATCH /namespaces/acme/inbound-ip-allowlist") != 0 {
		t.Fatal("changing only deletion_protection must not call the API")
	}
}

func TestOriginInboundIPAllowlistDeleteProtection(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.add("acme", "203.0.113.0/24", "Office", true)
	mock.setEnabled("acme", true)

	ctx := context.Background()
	res := &originInboundIPAllowlistResource{client: mock.client()}
	protected := originInboundIPAllowlistModel{ID: types.StringValue("acme"), Namespace: types.StringValue("acme"), Enabled: types.BoolValue(true), DeletionProtection: types.BoolValue(true)}
	resp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: inboundAllowlistState(t, res, protected)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "deletion_protection") {
		t.Fatalf("diagnostics = %v, want deletion_protection to block delete", resp.Diagnostics)
	}
	nullFlag := protected
	nullFlag.DeletionProtection = types.BoolNull()
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: inboundAllowlistState(t, res, nullFlag)}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a null deletion_protection must be treated as protected")
	}
	if mock.count("PATCH /namespaces/acme/inbound-ip-allowlist") != 0 || !mock.enabled("acme") {
		t.Fatal("enforcement was turned off while protected")
	}

	unprotected := protected
	unprotected.DeletionProtection = types.BoolValue(false)
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: inboundAllowlistState(t, res, unprotected)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unprotected delete: %v", resp.Diagnostics)
	}
	if body := mock.lastBody("PATCH /namespaces/acme/inbound-ip-allowlist"); body != `{"enabled":false}` || mock.enabled("acme") {
		t.Fatalf("delete body = %s, want enforcement off once deletion_protection is false", body)
	}
}

func TestOriginInboundIPAllowlistModifyPlanRespectsDeletionProtection(t *testing.T) {
	ctx := context.Background()
	res := &originInboundIPAllowlistResource{}
	sch := inboundAllowlistSchema(t, res)
	nullPlan := tfsdk.Plan{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
	protected := originInboundIPAllowlistModel{ID: types.StringValue("acme"), Namespace: types.StringValue("acme"), Enabled: types.BoolValue(true), DeletionProtection: types.BoolValue(true)}

	resp := &resource.ModifyPlanResponse{Plan: nullPlan}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: nullPlan, State: inboundAllowlistState(t, res, protected)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "deletion_protection") {
		t.Fatalf("diagnostics = %v, want the destroy plan refused while protected", resp.Diagnostics)
	}

	modify := func(state, plan originInboundIPAllowlistModel) *resource.ModifyPlanResponse {
		planValue := tfsdk.Plan{Schema: sch}
		if diags := planValue.Set(ctx, &plan); diags.HasError() {
			t.Fatal(diags)
		}
		resp := &resource.ModifyPlanResponse{Plan: planValue}
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: inboundAllowlistState(t, res, state)}, resp)
		return resp
	}
	moved := protected
	moved.Namespace = types.StringValue("other")
	if resp := modify(protected, moved); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "changing namespace") {
		t.Fatalf("diagnostics = %v, want the namespace change refused while protected", resp.Diagnostics)
	}
	unknownNamespace := protected
	unknownNamespace.Namespace = types.StringUnknown()
	if resp := modify(protected, unknownNamespace); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "namespace is not known until apply") {
		t.Fatalf("diagnostics = %v, want an unknown namespace refused while protected", resp.Diagnostics)
	}
	relaxed := protected
	relaxed.Enabled = types.BoolValue(false)
	if resp := modify(protected, relaxed); resp.Diagnostics.HasError() {
		t.Fatalf("enabled is an in-place update and must plan: %v", resp.Diagnostics)
	}

	unprotected := protected
	unprotected.DeletionProtection = types.BoolValue(false)
	resp = &resource.ModifyPlanResponse{Plan: nullPlan}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: nullPlan, State: inboundAllowlistState(t, res, unprotected)}, resp)
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

func inboundAllowlistSchema(t *testing.T, res *originInboundIPAllowlistResource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	res.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func inboundAllowlistState(t *testing.T, res *originInboundIPAllowlistResource, model originInboundIPAllowlistModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: inboundAllowlistSchema(t, res)}
	if diags := state.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return state
}
