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
	state, resp := readSSHCADataSource(t, ds, map[string]string{"namespace": "acme"})
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	if state.Namespace.ValueString() != "acme" || state.Owner.ValueString() != "acme" || !state.RequireCertificates.ValueBool() || len(state.CertificateAuthorities) != 2 {
		t.Fatalf("state = %#v", state)
	}
	if got := state.CertificateAuthorities[0]; got.ID.ValueString() != second.ID || got.Name.ValueString() != "Acme staging CA" || got.Fingerprint.ValueString() != secondSSHCAFingerprint {
		t.Fatalf("newest authority = %#v", got)
	}
	if got := state.CertificateAuthorities[1]; got.ID.ValueString() != first.ID || got.PublicKey.ValueString() != sampleSSHCAKey || got.KeyType.ValueString() != "ssh-ed25519" || got.CreatedAt.ValueString() != first.CreatedAt {
		t.Fatalf("oldest authority = %#v", got)
	}

	legacy, resp := readSSHCADataSource(t, ds, map[string]string{"owner": "acme"})
	if resp.Diagnostics.HasError() || legacy.Namespace.ValueString() != "acme" || legacy.Owner.ValueString() != "acme" || len(legacy.CertificateAuthorities) != 2 {
		t.Fatalf("deprecated owner = %#v, %v", legacy, resp.Diagnostics)
	}
	if mock.count("GET /namespaces/acme/ssh-certificate-authorities") != 2 {
		t.Fatalf("list calls = %d, want both reads on the namespace path", mock.count("GET /namespaces/acme/ssh-certificate-authorities"))
	}

	empty, resp := readSSHCADataSource(t, ds, map[string]string{"namespace": "other"})
	if resp.Diagnostics.HasError() || empty.RequireCertificates.ValueBool() || len(empty.CertificateAuthorities) != 0 {
		t.Fatalf("empty namespace = %#v, %v", empty, resp.Diagnostics)
	}

	for name, config := range map[string]map[string]string{
		"slash":    {"namespace": "acme/rocket"},
		"neither":  {},
		"mismatch": {"namespace": "acme", "owner": "other"},
	} {
		if _, resp := readSSHCADataSource(t, ds, config); !resp.Diagnostics.HasError() {
			t.Fatalf("%s: expected namespace validation error", name)
		}
	}
}

func readSSHCADataSource(t *testing.T, ds *originSSHCertificateAuthoritiesDataSource, slugs map[string]string) (originSSHCertificateAuthoritiesDataSourceModel, *datasource.ReadResponse) {
	t.Helper()
	ctx := context.Background()
	schemaResp := &datasource.SchemaResponse{}
	ds.Schema(ctx, datasource.SchemaRequest{}, schemaResp)
	objType := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	values := map[string]tftypes.Value{
		"namespace":               tftypes.NewValue(tftypes.String, nil),
		"owner":                   tftypes.NewValue(tftypes.String, nil),
		"require_certificates":    tftypes.NewValue(tftypes.Bool, nil),
		"certificate_authorities": tftypes.NewValue(objType.AttributeTypes["certificate_authorities"], nil),
	}
	for name, slug := range slugs {
		values[name] = tftypes.NewValue(tftypes.String, slug)
	}
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objType, values)}
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
