package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type originOwnerGrantsDataSource struct {
	client *apiClient
}

type originOwnerGrantsDataSourceModel struct {
	Owner  types.String              `tfsdk:"owner"`
	Grants []originGrantElementModel `tfsdk:"grants"`
}

func NewOriginOwnerGrantsDataSource() datasource.DataSource {
	return &originOwnerGrantsDataSource{}
}

func (d *originOwnerGrantsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_owner_grants"
}

func (d *originOwnerGrantsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the direct grants on an Origin owner namespace. Grants on individual repositories are not included. Uses the provider auth token.",
		Attributes: map[string]schema.Attribute{
			"owner": schema.StringAttribute{
				Required:    true,
				Description: "Owner slug: the team or user namespace.",
			},
			"grants": originGrantsDataSchema("Direct grants on the namespace. permission is read, contributor, write, admin, or custom."),
		},
	}
}

func (d *originOwnerGrantsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *originOwnerGrantsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config originOwnerGrantsDataSourceModel
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

	owner := config.Owner.ValueString()
	grants, err := d.client.listOriginGrants(ctx, originOwnerGrantsPath(owner))
	if err != nil {
		resp.Diagnostics.AddError("Failed to list Origin owner grants", err.Error())
		return
	}
	elements, err := originGrantElementsFromWire(owner, grants, ownerPermissionFromWire)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list Origin owner grants", err.Error())
		return
	}
	config.Grants = elements
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
