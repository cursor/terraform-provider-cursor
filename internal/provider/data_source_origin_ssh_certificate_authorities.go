package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type originSSHCertificateAuthoritiesDataSource struct {
	client *apiClient
}

type originSSHCertificateAuthoritiesDataSourceModel struct {
	Owner                  types.String                                `tfsdk:"owner"`
	RequireCertificates    types.Bool                                  `tfsdk:"require_certificates"`
	CertificateAuthorities []originSSHCertificateAuthorityElementModel `tfsdk:"certificate_authorities"`
}

type originSSHCertificateAuthorityElementModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	KeyType     types.String `tfsdk:"key_type"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	PublicKey   types.String `tfsdk:"public_key"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func NewOriginSSHCertificateAuthoritiesDataSource() datasource.DataSource {
	return &originSSHCertificateAuthoritiesDataSource{}
}

func (d *originSSHCertificateAuthoritiesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_ssh_certificate_authorities"
}

func (d *originSSHCertificateAuthoritiesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the SSH certificate authorities an Origin owner trusts, newest first, and whether the owner requires certificates. Uses the provider auth token.",
		Attributes: map[string]schema.Attribute{
			"owner": schema.StringAttribute{
				Required:    true,
				Description: "Owner slug of the team namespace.",
			},
			"require_certificates": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the owner requires SSH certificates for git over SSH.",
			},
			"certificate_authorities": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every authority the owner trusts, newest first.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:    true,
							Description: "Origin-assigned authority ID (nsca_...).",
						},
						"name": schema.StringAttribute{
							Computed:    true,
							Description: "Label given when the authority was added.",
						},
						"key_type": schema.StringAttribute{
							Computed:    true,
							Description: "OpenSSH key type of the public key, for example ssh-ed25519.",
						},
						"fingerprint": schema.StringAttribute{
							Computed:    true,
							Description: "SHA-256 fingerprint of the public key as SHA256:<base64>.",
						},
						"public_key": schema.StringAttribute{
							Computed:    true,
							Description: "The authority's public key as <key_type> <base64>, without a comment.",
						},
						"created_at": schema.StringAttribute{
							Computed:    true,
							Description: "RFC 3339 time the authority was added.",
						},
					},
				},
			},
		},
	}
}

func (d *originSSHCertificateAuthoritiesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*apiClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *apiClient, got %T", req.ProviderData))
		return
	}
	d.client = client
}

func (d *originSSHCertificateAuthoritiesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config originSSHCertificateAuthoritiesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if err := requireIDSafeSlug(config.Owner, "owner"); err != nil {
		resp.Diagnostics.AddError("Invalid Origin owner", err.Error())
		return
	}

	list, err := d.client.listOriginSSHCertificateAuthorities(ctx, config.Owner.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to list Origin SSH certificate authorities", err.Error())
		return
	}
	config.RequireCertificates = types.BoolValue(list.RequireCertificates)
	config.CertificateAuthorities = make([]originSSHCertificateAuthorityElementModel, 0, len(list.CertificateAuthorities))
	for _, authority := range list.CertificateAuthorities {
		config.CertificateAuthorities = append(config.CertificateAuthorities, originSSHCertificateAuthorityElementModel{
			ID:          types.StringValue(authority.ID),
			Name:        types.StringValue(authority.Name),
			KeyType:     types.StringValue(authority.KeyType),
			Fingerprint: types.StringValue(authority.Fingerprint),
			PublicKey:   types.StringValue(authority.PublicKey),
			CreatedAt:   types.StringValue(authority.CreatedAt),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
