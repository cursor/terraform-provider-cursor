package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestOriginInboundIPAllowlistEntriesDataSourceRead(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	office := mock.add("acme", "203.0.113.0/24", "Office", true)
	vpn := mock.add("acme", "198.51.100.7", "", false)
	mock.setEnabled("acme", true)

	ds := &originInboundIPAllowlistEntriesDataSource{client: mock.client()}
	state, resp := readInboundAllowlistDataSource(t, ds, "acme")
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	if !state.Enabled.ValueBool() || len(state.Entries) != 2 {
		t.Fatalf("state = %#v", state)
	}
	if got := state.Entries[0]; got.ID.ValueString() != office.ID || got.CIDR.ValueString() != "203.0.113.0/24" || got.Description.ValueString() != "Office" || !got.Enabled.ValueBool() || got.CreatedAt.ValueString() != office.CreatedAt {
		t.Fatalf("oldest entry = %#v", got)
	}
	if got := state.Entries[1]; got.ID.ValueString() != vpn.ID || got.CIDR.ValueString() != "198.51.100.7" || got.Description.ValueString() != "" || got.Enabled.ValueBool() {
		t.Fatalf("newest entry = %#v", got)
	}

	empty, resp := readInboundAllowlistDataSource(t, ds, "other")
	if resp.Diagnostics.HasError() || empty.Enabled.ValueBool() || empty.Entries == nil || len(empty.Entries) != 0 {
		t.Fatalf("empty namespace = %#v, %v", empty, resp.Diagnostics)
	}

	mock.hide("hidden")
	if _, resp := readInboundAllowlistDataSource(t, ds, "hidden"); !resp.Diagnostics.HasError() {
		t.Fatal("expected the namespace 404 surfaced")
	}
	if _, resp := readInboundAllowlistDataSource(t, ds, "acme/rocket"); !resp.Diagnostics.HasError() {
		t.Fatal("expected slug validation error")
	}
}

func readInboundAllowlistDataSource(t *testing.T, ds *originInboundIPAllowlistEntriesDataSource, namespace string) (originInboundIPAllowlistEntriesDataSourceModel, *datasource.ReadResponse) {
	t.Helper()
	ctx := context.Background()
	schemaResp := &datasource.SchemaResponse{}
	ds.Schema(ctx, datasource.SchemaRequest{}, schemaResp)
	objType := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objType, map[string]tftypes.Value{
		"namespace": tftypes.NewValue(tftypes.String, namespace),
		"enabled":   tftypes.NewValue(tftypes.Bool, nil),
		"entries":   tftypes.NewValue(objType.AttributeTypes["entries"], nil),
	})}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	ds.Read(ctx, datasource.ReadRequest{Config: config}, resp)
	var state originInboundIPAllowlistEntriesDataSourceModel
	if !resp.Diagnostics.HasError() {
		if diags := resp.State.Get(ctx, &state); diags.HasError() {
			t.Fatal(diags)
		}
	}
	return state, resp
}
