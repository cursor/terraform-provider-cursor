package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestOriginSSHCertificateAuthoritiesDataSourceRead(t *testing.T) {
	mock := newSSHCAMock(t)
	defer mock.Close()
	first := mock.add("acme", sampleSSHCAKey, "Acme production CA")
	second := mock.add("acme", secondSSHCAKey, "Acme staging CA")
	mock.setRequire("acme", true)

	ds := &originSSHCertificateAuthoritiesDataSource{client: mock.client()}
	state, resp := readSSHCADataSource(t, ds, "acme")
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	if !state.RequireCertificates.ValueBool() || len(state.CertificateAuthorities) != 2 {
		t.Fatalf("state = %#v", state)
	}
	if got := state.CertificateAuthorities[0]; got.ID.ValueString() != second.ID || got.Name.ValueString() != "Acme staging CA" || got.Fingerprint.ValueString() != secondSSHCAFingerprint {
		t.Fatalf("newest authority = %#v", got)
	}
	if got := state.CertificateAuthorities[1]; got.ID.ValueString() != first.ID || got.PublicKey.ValueString() != sampleSSHCAKey || got.KeyType.ValueString() != "ssh-ed25519" || got.CreatedAt.ValueString() != first.CreatedAt {
		t.Fatalf("oldest authority = %#v", got)
	}

	empty, resp := readSSHCADataSource(t, ds, "other")
	if resp.Diagnostics.HasError() || empty.RequireCertificates.ValueBool() || len(empty.CertificateAuthorities) != 0 {
		t.Fatalf("empty owner = %#v, %v", empty, resp.Diagnostics)
	}

	if _, resp := readSSHCADataSource(t, ds, "acme/rocket"); !resp.Diagnostics.HasError() {
		t.Fatal("expected slug validation error")
	}
}

func readSSHCADataSource(t *testing.T, ds *originSSHCertificateAuthoritiesDataSource, owner string) (originSSHCertificateAuthoritiesDataSourceModel, *datasource.ReadResponse) {
	t.Helper()
	ctx := context.Background()
	schemaResp := &datasource.SchemaResponse{}
	ds.Schema(ctx, datasource.SchemaRequest{}, schemaResp)
	objType := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objType, map[string]tftypes.Value{
		"owner":                   tftypes.NewValue(tftypes.String, owner),
		"require_certificates":    tftypes.NewValue(tftypes.Bool, nil),
		"certificate_authorities": tftypes.NewValue(objType.AttributeTypes["certificate_authorities"], nil),
	})}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	ds.Read(ctx, datasource.ReadRequest{Config: config}, resp)
	var state originSSHCertificateAuthoritiesDataSourceModel
	if !resp.Diagnostics.HasError() {
		if diags := resp.State.Get(ctx, &state); diags.HasError() {
			t.Fatal(diags)
		}
	}
	return state, resp
}
