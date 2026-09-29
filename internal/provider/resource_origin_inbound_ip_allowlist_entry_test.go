package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestOriginInboundIPAllowlistEntryCreate(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()

	res := &originInboundIPAllowlistEntryResource{client: mock.client()}
	plan := sampleInboundEntryModel()
	plan.ID, plan.CreatedAt = types.StringUnknown(), types.StringUnknown()
	got := createInboundEntry(t, res, plan)
	if body := mock.lastBody("POST /namespaces/acme/inbound-ip-allowlist/entries"); body != `{"cidr":"203.0.113.0/24","description":"Office","enabled":true}` {
		t.Fatalf("create body = %s", body)
	}
	if got.ID.ValueString() != "nsip_00000000000000000000000001" || got.CIDR.ValueString() != "203.0.113.0/24" || got.Description.ValueString() != "Office" || !got.Enabled.ValueBool() || got.CreatedAt.ValueString() != "2026-08-02T14:45:01Z" {
		t.Fatalf("state = %#v", got)
	}

	disabled := sampleInboundEntryModel()
	disabled.ID, disabled.CreatedAt = types.StringUnknown(), types.StringUnknown()
	disabled.CIDR, disabled.Description, disabled.Enabled = types.StringValue("2001:db8::/32"), types.StringValue(""), types.BoolValue(false)
	got = createInboundEntry(t, res, disabled)
	if body := mock.lastBody("POST /namespaces/acme/inbound-ip-allowlist/entries"); body != `{"cidr":"2001:db8::/32","enabled":false}` {
		t.Fatalf("disabled create body = %s", body)
	}
	if got.Enabled.ValueBool() || got.Description.ValueString() != "" {
		t.Fatalf("disabled state = %#v", got)
	}
}

func TestOriginInboundIPAllowlistEntryCreateSurfacesAPIErrors(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.add("acme", "203.0.113.0/24", "existing", true)

	ctx := context.Background()
	res := &originInboundIPAllowlistEntryResource{client: mock.client()}
	plan := sampleInboundEntryModel()
	plan.ID = types.StringUnknown()
	planValue := tfsdk.Plan{Schema: inboundEntrySchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: planValue.Schema}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "HTTP 409") {
		t.Fatalf("diagnostics = %v, want the 409 surfaced", resp.Diagnostics)
	}
}

func TestOriginInboundIPAllowlistEntryReadRefreshesChangesMadeElsewhere(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.add("acme", "198.51.100.7", "other", true)
	stored := mock.add("acme", "203.0.113.0/25", "Office, east wing", false)

	res := &originInboundIPAllowlistEntryResource{client: mock.client()}
	model := sampleInboundEntryModel()
	model.ID = types.StringValue(stored.ID)
	got := readInboundEntry(t, res, model)
	if got == nil {
		t.Fatal("read removed a listed entry")
	}
	if got.CIDR.ValueString() != "203.0.113.0/25" || got.Description.ValueString() != "Office, east wing" || got.Enabled.ValueBool() || got.CreatedAt.ValueString() != stored.CreatedAt {
		t.Fatalf("state = %#v", got)
	}
	if mock.count("GET /namespaces/acme/inbound-ip-allowlist/entries/"+stored.ID) != 1 || mock.count("GET /namespaces/acme/inbound-ip-allowlist") != 0 {
		t.Fatal("read must fetch the entry by id without listing the namespace")
	}
}

func TestOriginInboundIPAllowlistEntryReadFillsImportedEntry(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	stored := mock.add("acme", "203.0.113.0/24", "Office", true)

	res := &originInboundIPAllowlistEntryResource{client: mock.client()}
	got := readInboundEntry(t, res, originInboundIPAllowlistEntryModel{
		ID:                 types.StringValue(stored.ID),
		Namespace:          types.StringValue("acme"),
		CIDR:               types.StringNull(),
		Description:        types.StringNull(),
		Enabled:            types.BoolNull(),
		CreatedAt:          types.StringNull(),
		DeletionProtection: types.BoolNull(),
	})
	if got == nil || got.CIDR.ValueString() != "203.0.113.0/24" || got.Description.ValueString() != "Office" || !got.Enabled.ValueBool() || got.CreatedAt.ValueString() != stored.CreatedAt {
		t.Fatalf("state = %#v", got)
	}
	if !got.DeletionProtection.ValueBool() {
		t.Fatal("a null deletion_protection must read back as protected")
	}
}

func TestOriginInboundIPAllowlistEntryReadRemovesMissing(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.add("acme", "198.51.100.7", "other", true)

	res := &originInboundIPAllowlistEntryResource{client: mock.client()}
	if got := readInboundEntry(t, res, sampleInboundEntryModel()); got != nil {
		t.Fatalf("expected a removed entry to leave state, got %#v", got)
	}
}

func TestOriginInboundIPAllowlistEntryReadHiddenNamespaceKeepsState(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.hide("acme")

	res := &originInboundIPAllowlistEntryResource{client: mock.client()}
	state := inboundEntryState(t, res, sampleInboundEntryModel())
	resp := &resource.ReadResponse{State: state}
	res.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "not found or not visible") {
		t.Fatalf("diagnostics = %v, want the namespace 404 surfaced", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("read removed state when the namespace could not be read")
	}
}

func TestOriginInboundIPAllowlistEntryUpdateInPlace(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	stored := mock.add("acme", "203.0.113.0/24", "Office", true)

	res := &originInboundIPAllowlistEntryResource{client: mock.client()}
	route := "PATCH /namespaces/acme/inbound-ip-allowlist/entries/" + stored.ID
	state := sampleInboundEntryModel()
	state.ID, state.CreatedAt = types.StringValue(stored.ID), types.StringValue(stored.CreatedAt)

	moved := state
	moved.CIDR = types.StringValue("203.0.113.0/25")
	got := updateInboundEntry(t, res, state, moved)
	if body := mock.lastBody(route); body != `{"cidr":"203.0.113.0/25"}` {
		t.Fatalf("cidr body = %s", body)
	}
	if got.ID.ValueString() != stored.ID || got.CIDR.ValueString() != "203.0.113.0/25" || got.CreatedAt.ValueString() != stored.CreatedAt {
		t.Fatalf("state = %#v, want the same entry with the new cidr", got)
	}

	cleared := *got
	cleared.Description, cleared.Enabled = types.StringValue(""), types.BoolValue(false)
	got = updateInboundEntry(t, res, *got, cleared)
	if body := mock.lastBody(route); body != `{"description":"","enabled":false}` {
		t.Fatalf("description/enabled body = %s", body)
	}
	if got.Description.ValueString() != "" || got.Enabled.ValueBool() || got.CIDR.ValueString() != "203.0.113.0/25" {
		t.Fatalf("state = %#v", got)
	}
	if entries := mock.entries("acme"); len(entries) != 1 || entries[0].ID != stored.ID || entries[0].Enabled || entries[0].Description != "" {
		t.Fatalf("stored entries = %#v", entries)
	}
	if mock.count("POST /namespaces/acme/inbound-ip-allowlist/entries") != 0 || mock.count("DELETE /namespaces/acme/inbound-ip-allowlist/entries/"+stored.ID) != 0 {
		t.Fatal("an in-place update must not add or remove entries")
	}
}

func TestOriginInboundIPAllowlistEntryUpdateOfDeletionProtectionSkipsAPI(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()

	res := &originInboundIPAllowlistEntryResource{client: mock.client()}
	prior := sampleInboundEntryModel()
	prior.DeletionProtection = types.BoolValue(true)
	plan := prior
	plan.DeletionProtection = types.BoolValue(false)
	plan.ID, plan.CreatedAt = types.StringUnknown(), types.StringUnknown()
	got := updateInboundEntry(t, res, prior, plan)
	if got.DeletionProtection.ValueBool() || got.ID.ValueString() != sampleInboundIPEntryID || got.CreatedAt.ValueString() != "2026-08-02T14:45:00Z" || got.CIDR.ValueString() != "203.0.113.0/24" {
		t.Fatalf("state = %#v", got)
	}
	if mock.count("PATCH /namespaces/acme/inbound-ip-allowlist/entries/"+sampleInboundIPEntryID) != 0 {
		t.Fatal("changing only deletion_protection must not call the API")
	}
}

func TestOriginInboundIPAllowlistEntryDeleteProtection(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	stored := mock.add("acme", "203.0.113.0/24", "Office", true)

	ctx := context.Background()
	res := &originInboundIPAllowlistEntryResource{client: mock.client()}
	route := "DELETE /namespaces/acme/inbound-ip-allowlist/entries/" + stored.ID
	protected := sampleInboundEntryModel()
	protected.ID = types.StringValue(stored.ID)
	protected.DeletionProtection = types.BoolValue(true)
	resp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: inboundEntryState(t, res, protected)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "deletion_protection") {
		t.Fatalf("diagnostics = %v, want deletion_protection to block delete", resp.Diagnostics)
	}
	nullFlag := protected
	nullFlag.DeletionProtection = types.BoolNull()
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: inboundEntryState(t, res, nullFlag)}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a null deletion_protection must be treated as protected")
	}
	if mock.count(route) != 0 {
		t.Fatal("delete request was sent while protected")
	}

	unprotected := protected
	unprotected.DeletionProtection = types.BoolValue(false)
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: inboundEntryState(t, res, unprotected)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unprotected delete: %v", resp.Diagnostics)
	}
	if mock.count(route) != 1 || len(mock.entries("acme")) != 0 {
		t.Fatal("expected the entry removed once deletion_protection is false")
	}
}

func TestOriginInboundIPAllowlistEntryDeleteRefusedWhileItAdmitsCallerThenNotFound(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.setCaller("203.0.113.10")
	stored := mock.add("acme", "203.0.113.0/24", "Office", true)
	mock.add("acme", "198.51.100.7", "VPN egress", true)
	mock.setEnabled("acme", true)

	ctx := context.Background()
	res := &originInboundIPAllowlistEntryResource{client: mock.client()}
	model := sampleInboundEntryModel()
	model.ID = types.StringValue(stored.ID)
	state := inboundEntryState(t, res, model)

	resp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: state}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "exclude your own address") {
		t.Fatalf("diagnostics = %v, want the INVALID_ARGUMENT surfaced", resp.Diagnostics)
	}

	mock.setEnabled("acme", false)
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete after enforcement stopped: %v", resp.Diagnostics)
	}

	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("deleting a removed entry must succeed: %v", resp.Diagnostics)
	}
	if got := mock.count("DELETE /namespaces/acme/inbound-ip-allowlist/entries/" + stored.ID); got != 3 {
		t.Fatalf("delete calls = %d", got)
	}
}

func TestOriginInboundIPAllowlistEntryDestroyPlanRespectsDeletionProtection(t *testing.T) {
	ctx := context.Background()
	res := &originInboundIPAllowlistEntryResource{}
	sch := inboundEntrySchema(t, res)
	nullPlan := tfsdk.Plan{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}

	protected := sampleInboundEntryModel()
	protected.DeletionProtection = types.BoolValue(true)
	resp := &resource.ModifyPlanResponse{Plan: nullPlan}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: nullPlan, State: inboundEntryState(t, res, protected)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "deletion_protection") {
		t.Fatalf("diagnostics = %v, want the destroy plan refused while protected", resp.Diagnostics)
	}

	unprotected := protected
	unprotected.DeletionProtection = types.BoolValue(false)
	resp = &resource.ModifyPlanResponse{Plan: nullPlan}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: nullPlan, State: inboundEntryState(t, res, unprotected)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unprotected destroy plan: %v", resp.Diagnostics)
	}
}

func TestOriginInboundIPAllowlistEntryReplacementRespectsDeletionProtection(t *testing.T) {
	ctx := context.Background()
	res := &originInboundIPAllowlistEntryResource{}
	sch := inboundEntrySchema(t, res)
	protected := sampleInboundEntryModel()
	protected.DeletionProtection = types.BoolValue(true)

	modify := func(state, plan originInboundIPAllowlistEntryModel) *resource.ModifyPlanResponse {
		planValue := tfsdk.Plan{Schema: sch}
		if diags := planValue.Set(ctx, &plan); diags.HasError() {
			t.Fatal(diags)
		}
		resp := &resource.ModifyPlanResponse{Plan: planValue}
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, State: inboundEntryState(t, res, state)}, resp)
		return resp
	}

	moved := protected
	moved.Namespace = types.StringValue("other")
	if resp := modify(protected, moved); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "changing namespace") {
		t.Fatalf("diagnostics = %v, want the namespace change refused while protected", resp.Diagnostics)
	}
	moved.DeletionProtection = types.BoolValue(false)
	if resp := modify(protected, moved); !resp.Diagnostics.HasError() {
		t.Fatal("a namespace change with deletion_protection = false only in the plan must be refused")
	}
	unknownNamespace := protected
	unknownNamespace.Namespace = types.StringUnknown()
	if resp := modify(protected, unknownNamespace); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "namespace is not known until apply") {
		t.Fatalf("diagnostics = %v, want an unknown namespace refused while protected", resp.Diagnostics)
	}

	edited := protected
	edited.CIDR, edited.Description, edited.Enabled = types.StringValue("203.0.113.0/25"), types.StringValue("Office, east wing"), types.BoolValue(false)
	if resp := modify(protected, edited); resp.Diagnostics.HasError() {
		t.Fatalf("cidr, description, and enabled are in-place updates and must plan: %v", resp.Diagnostics)
	}
	unknownCIDR := protected
	unknownCIDR.CIDR = types.StringUnknown()
	if resp := modify(protected, unknownCIDR); resp.Diagnostics.HasError() {
		t.Fatalf("an unknown cidr is still an in-place update and must plan: %v", resp.Diagnostics)
	}

	unprotect := protected
	unprotect.DeletionProtection = types.BoolValue(false)
	if resp := modify(protected, unprotect); resp.Diagnostics.HasError() {
		t.Fatalf("turning deletion_protection off must plan: %v", resp.Diagnostics)
	}
	if resp := modify(unprotect, moved); resp.Diagnostics.HasError() {
		t.Fatalf("namespace change after deletion_protection = false was applied must plan: %v", resp.Diagnostics)
	}
	unknownNamespace.DeletionProtection = types.BoolValue(false)
	if resp := modify(unprotect, unknownNamespace); resp.Diagnostics.HasError() {
		t.Fatalf("unknown namespace after deletion_protection = false was applied must plan: %v", resp.Diagnostics)
	}

	create := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: sch}}
	if diags := create.Plan.Set(ctx, &protected); diags.HasError() {
		t.Fatal(diags)
	}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: create.Plan, State: emptyState(ctx, sch)}, create)
	if create.Diagnostics.HasError() {
		t.Fatalf("create must plan: %v", create.Diagnostics)
	}
}

func TestOriginInboundIPAllowlistEntryImportState(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	office := mock.add("acme", "203.0.113.0/24", "Office", true)
	v6 := mock.add("acme", "2001:db8::/32", "", true)

	ctx := context.Background()
	res := &originInboundIPAllowlistEntryResource{client: mock.client()}
	sch := inboundEntrySchema(t, res)
	importEntry := func(id string) (originInboundIPAllowlistEntryModel, *resource.ImportStateResponse) {
		resp := &resource.ImportStateResponse{State: emptyState(ctx, sch)}
		res.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
		var model originInboundIPAllowlistEntryModel
		if !resp.Diagnostics.HasError() {
			if diags := resp.State.Get(ctx, &model); diags.HasError() {
				t.Fatal(diags)
			}
		}
		return model, resp
	}

	got, resp := importEntry("acme:" + office.ID)
	if resp.Diagnostics.HasError() || got.Namespace.ValueString() != "acme" || got.ID.ValueString() != office.ID || !got.CIDR.IsNull() || !got.DeletionProtection.ValueBool() {
		t.Fatalf("import by id = %#v, %v", got, resp.Diagnostics)
	}
	if mock.count("GET /namespaces/acme/inbound-ip-allowlist") != 0 {
		t.Fatal("import by id must not list the namespace")
	}
	got, resp = importEntry("acme:203.0.113.0/24")
	if resp.Diagnostics.HasError() || got.ID.ValueString() != office.ID {
		t.Fatalf("import by cidr = %#v, %v", got, resp.Diagnostics)
	}
	got, resp = importEntry("acme:2001:db8::/32")
	if resp.Diagnostics.HasError() || got.ID.ValueString() != v6.ID || got.Namespace.ValueString() != "acme" {
		t.Fatalf("import by IPv6 cidr = %#v, %v", got, resp.Diagnostics)
	}
	if mock.count("GET /namespaces/acme/inbound-ip-allowlist") != 2 {
		t.Fatal("each import by cidr lists the namespace once")
	}
	if _, resp = importEntry("acme:198.51.100.7"); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "no inbound IP allowlist entry with cidr 198.51.100.7") {
		t.Fatalf("diagnostics = %v, want unknown cidr", resp.Diagnostics)
	}
	for _, id := range []string{"acme", "acme:", ":nsip_01", "acme/rocket:nsip_01", " acme:nsip_01", "acme: 203.0.113.0/24", "acme:nsip_01 "} {
		if _, resp = importEntry(id); !resp.Diagnostics.HasError() {
			t.Fatalf("import %q must be rejected", id)
		}
	}
}

func TestValidateOriginInboundIPAllowlistEntry(t *testing.T) {
	valid := sampleInboundEntryModel()
	if err := validateOriginInboundIPAllowlistEntry(valid); err != nil {
		t.Fatalf("valid model rejected: %v", err)
	}
	for _, cidr := range []string{"203.0.113.7", "203.0.113.7/24", "2001:db8::/32", "2001:db8::1", "::ffff:203.0.113.7", "203.0.113.7/32"} {
		model := sampleInboundEntryModel()
		model.CIDR = types.StringValue(cidr)
		if err := validateOriginInboundIPAllowlistEntry(model); err != nil {
			t.Errorf("cidr %q rejected: %v", cidr, err)
		}
	}
	unknown := valid
	unknown.Namespace, unknown.CIDR, unknown.Description = types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	if err := validateOriginInboundIPAllowlistEntry(unknown); err != nil {
		t.Fatalf("unknown values rejected: %v", err)
	}
	null := valid
	null.Description = types.StringNull()
	if err := validateOriginInboundIPAllowlistEntry(null); err != nil {
		t.Fatalf("null description rejected: %v", err)
	}
	long := valid
	long.Description = types.StringValue(strings.Repeat("é", 255))
	if err := validateOriginInboundIPAllowlistEntry(long); err != nil {
		t.Fatalf("255-character description rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*originInboundIPAllowlistEntryModel)
		want   string
	}{
		{"namespace with colon", func(m *originInboundIPAllowlistEntryModel) { m.Namespace = types.StringValue("acme:x") }, "namespace must not contain"},
		{"empty namespace", func(m *originInboundIPAllowlistEntryModel) { m.Namespace = types.StringValue("") }, "namespace is required"},
		{"null cidr", func(m *originInboundIPAllowlistEntryModel) { m.CIDR = types.StringNull() }, "cidr is required"},
		{"padded cidr", func(m *originInboundIPAllowlistEntryModel) { m.CIDR = types.StringValue(" 203.0.113.0/24") }, "cidr must not have"},
		{"not an address", func(m *originInboundIPAllowlistEntryModel) { m.CIDR = types.StringValue("office") }, "must be an IPv4 or IPv6 address"},
		{"prefix too long", func(m *originInboundIPAllowlistEntryModel) { m.CIDR = types.StringValue("203.0.113.0/33") }, "must be an IPv4 or IPv6 address"},
		{"zone", func(m *originInboundIPAllowlistEntryModel) { m.CIDR = types.StringValue("fe80::1%eth0") }, "must be an IPv4 or IPv6 address"},
		{"ipv4 /0", func(m *originInboundIPAllowlistEntryModel) { m.CIDR = types.StringValue("0.0.0.0/0") }, "entire address space"},
		{"ipv6 /0", func(m *originInboundIPAllowlistEntryModel) { m.CIDR = types.StringValue("::/0") }, "entire address space"},
		{"padded description", func(m *originInboundIPAllowlistEntryModel) { m.Description = types.StringValue("Office ") }, "description must not have"},
		{"long description", func(m *originInboundIPAllowlistEntryModel) {
			m.Description = types.StringValue(strings.Repeat("a", 256))
		}, "at most 255"},
	}
	for _, tc := range cases {
		model := sampleInboundEntryModel()
		tc.mutate(&model)
		err := validateOriginInboundIPAllowlistEntry(model)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestOriginInboundIPAllowlistSchemas(t *testing.T) {
	ctx := context.Background()
	entry := inboundEntrySchema(t, &originInboundIPAllowlistEntryResource{})
	if diags := entry.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("entry schema implementation: %v", diags)
	}
	if len(entry.Attributes["namespace"].(schema.StringAttribute).PlanModifiers) != 1 {
		t.Fatal("entry.namespace must require replace")
	}
	if len(entry.Attributes["cidr"].(schema.StringAttribute).PlanModifiers) != 0 {
		t.Fatal("entry.cidr must update in place; the API keeps the entry ID")
	}
	if entry.Attributes["enabled"].(schema.BoolAttribute).Default == nil || entry.Attributes["description"].(schema.StringAttribute).Default == nil {
		t.Fatal("entry.enabled and entry.description must default like the API")
	}
	allowlist := inboundAllowlistSchema(t, &originInboundIPAllowlistResource{})
	if diags := allowlist.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("allowlist schema implementation: %v", diags)
	}
	if len(allowlist.Attributes["enabled"].(schema.BoolAttribute).PlanModifiers) != 0 {
		t.Fatal("allowlist.enabled must update in place")
	}
	for name, sch := range map[string]schema.Schema{"entry": entry, "allowlist": allowlist} {
		protection := sch.Attributes["deletion_protection"].(schema.BoolAttribute)
		if !protection.Optional || !protection.Computed || protection.Default == nil {
			t.Fatalf("%s.deletion_protection must be optional and default to true, like origin_repo_ruleset", name)
		}
	}
}

func sampleInboundEntryModel() originInboundIPAllowlistEntryModel {
	return originInboundIPAllowlistEntryModel{
		ID:                 types.StringValue(sampleInboundIPEntryID),
		Namespace:          types.StringValue("acme"),
		CIDR:               types.StringValue("203.0.113.0/24"),
		Description:        types.StringValue("Office"),
		Enabled:            types.BoolValue(true),
		CreatedAt:          types.StringValue("2026-08-02T14:45:00Z"),
		DeletionProtection: types.BoolValue(false),
	}
}

func inboundEntrySchema(t *testing.T, res *originInboundIPAllowlistEntryResource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	res.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func inboundEntryState(t *testing.T, res *originInboundIPAllowlistEntryResource, model originInboundIPAllowlistEntryModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: inboundEntrySchema(t, res)}
	if diags := state.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return state
}

func createInboundEntry(t *testing.T, res *originInboundIPAllowlistEntryResource, plan originInboundIPAllowlistEntryModel) originInboundIPAllowlistEntryModel {
	t.Helper()
	ctx := context.Background()
	planValue := tfsdk.Plan{Schema: inboundEntrySchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: planValue.Schema}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create diagnostics: %v", resp.Diagnostics)
	}
	var got originInboundIPAllowlistEntryModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	return got
}

func updateInboundEntry(t *testing.T, res *originInboundIPAllowlistEntryResource, state, plan originInboundIPAllowlistEntryModel) *originInboundIPAllowlistEntryModel {
	t.Helper()
	ctx := context.Background()
	planValue := tfsdk.Plan{Schema: inboundEntrySchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: planValue.Schema}}
	res.Update(ctx, resource.UpdateRequest{Plan: planValue, State: inboundEntryState(t, res, state)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update diagnostics: %v", resp.Diagnostics)
	}
	var got originInboundIPAllowlistEntryModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	return &got
}

func readInboundEntry(t *testing.T, res *originInboundIPAllowlistEntryResource, model originInboundIPAllowlistEntryModel) *originInboundIPAllowlistEntryModel {
	t.Helper()
	ctx := context.Background()
	state := inboundEntryState(t, res, model)
	resp := &resource.ReadResponse{State: state}
	res.Read(ctx, resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		return nil
	}
	var got originInboundIPAllowlistEntryModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	return &got
}
