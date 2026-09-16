package provider

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	envToken      = "CURSOR_TOKEN"
	envEndpoint   = "CURSOR_ENDPOINT"
	envTeamAPIKey = "CURSOR_TEAM_API_KEY"
	envOrgAPIKey  = "CURSOR_ORGANIZATION_API_KEY"

	defaultEndpoint = "https://api2.cursor.sh"
)

type cursorProvider struct {
	version string
}

type cursorProviderModel struct {
	Token              types.String `tfsdk:"token"`
	Endpoint           types.String `tfsdk:"endpoint"`
	TeamAPIKey         types.String `tfsdk:"team_api_key"`
	OrganizationAPIKey types.String `tfsdk:"organization_api_key"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &cursorProvider{version: version}
	}
}

func (p *cursorProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "cursor"
	resp.Version = p.version
}

func (p *cursorProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Cursor Automations, Origin repository rulesets, and Origin repository and owner grants, and read Origin repositories. Automations use the Cursor Automations API over Connect RPC. Origin calls use the public Origin API and the same auth token. Grants keyed by user_email or group_name also need the Team or Organization Admin API key.",
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Auth token for the Cursor API. Can also be set via CURSOR_TOKEN.",
			},
			"endpoint": schema.StringAttribute{
				Optional:    true,
				Description: fmt.Sprintf("Cursor API base URL. Defaults to %s. Can also be set via CURSOR_ENDPOINT.", defaultEndpoint),
			},
			"team_api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: fmt.Sprintf("Team Admin API key, sent as HTTP Basic auth (key as username, empty password) to %s%s. Required only to resolve user_email on Origin grants. Can also be set via %s.", defaultAdminAPIBase, adminTeamMembersPath, envTeamAPIKey),
			},
			"organization_api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: fmt.Sprintf("Organization Admin API key, sent as HTTP Basic auth (key as username, empty password) to %s%s. Required only to resolve group_name on Origin grants. Can also be set via %s.", defaultAdminAPIBase, adminOrgGroupsPath, envOrgAPIKey),
			},
		},
	}
}

func (p *cursorProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config cursorProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	token := getStringValue(config.Token, envToken)
	if token == "" {
		resp.Diagnostics.AddError(
			"Missing Cursor API token",
			fmt.Sprintf("Set the provider \"token\" attribute or the %s environment variable.", envToken),
		)
		return
	}

	endpoint := getStringValue(config.Endpoint, envEndpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	endpoint = strings.TrimRight(endpoint, "/")

	client, err := newAPIClient(endpoint, token, p.version, adminKeys{
		team:         getStringValue(config.TeamAPIKey, envTeamAPIKey),
		organization: getStringValue(config.OrganizationAPIKey, envOrgAPIKey),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to configure Cursor client", err.Error())
		return
	}

	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *cursorProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewPlatformWorkflowResource,
		NewOriginRepoRulesetResource,
		NewOriginRepoGrantResource,
		NewOriginOwnerGrantResource,
	}
}

func (p *cursorProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewPlatformWorkflowDataSource,
		NewOriginRepoDataSource,
		NewOriginRepoGrantsDataSource,
		NewOriginOwnerGrantsDataSource,
	}
}

func getStringValue(value types.String, envKey string) string {
	if !value.IsNull() && !value.IsUnknown() {
		return strings.TrimSpace(value.ValueString())
	}
	return strings.TrimSpace(os.Getenv(envKey))
}
