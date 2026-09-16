package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type originRepoGrantsDataSource struct {
	client *apiClient
}

type originRepoGrantsDataSourceModel struct {
	Owner  types.String              `tfsdk:"owner"`
	Repo   types.String              `tfsdk:"repo"`
	Grants []originGrantElementModel `tfsdk:"grants"`
}

func NewOriginRepoGrantsDataSource() datasource.DataSource {
	return &originRepoGrantsDataSource{}
}

func (d *originRepoGrantsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_repo_grants"
}

func (d *originRepoGrantsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the direct grants on an Origin repository. Access inherited from namespace grants is not included. Uses the provider auth token.",
		Attributes: map[string]schema.Attribute{
			"owner": schema.StringAttribute{
				Required:    true,
				Description: "Owner slug of the repository.",
			},
			"repo": schema.StringAttribute{
				Required:    true,
				Description: "Repository name within the owner.",
			},
			"grants": originGrantsDataSchema("Direct grants on the repository. permission is read, write, admin, or custom."),
		},
	}
}

func (d *originRepoGrantsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *originRepoGrantsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config originRepoGrantsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if err := requireIDSafeSlug(config.Owner, "owner"); err != nil {
		resp.Diagnostics.AddError("Invalid Origin repository", err.Error())
		return
	}
	if err := requireIDSafeSlug(config.Repo, "repo"); err != nil {
		resp.Diagnostics.AddError("Invalid Origin repository", err.Error())
		return
	}

	owner, repo := config.Owner.ValueString(), config.Repo.ValueString()
	grants, err := d.client.listOriginGrants(ctx, originRepoGrantsPath(owner, repo))
	if err != nil {
		resp.Diagnostics.AddError("Failed to list Origin repository grants", err.Error())
		return
	}
	elements, err := originGrantElementsFromWire(owner+"/"+repo, grants, repoPermissionFromWire)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list Origin repository grants", err.Error())
		return
	}
	config.Grants = elements
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
