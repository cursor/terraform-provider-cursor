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
	envToken    = "CURSOR_TOKEN"
	envEndpoint = "CURSOR_ENDPOINT"

	defaultEndpoint = "https://api2.cursor.sh"
)

type cursorProvider struct {
	version string
}

type cursorProviderModel struct {
	Token    types.String `tfsdk:"token"`
	Endpoint types.String `tfsdk:"endpoint"`
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
		Description: "Manage Cursor Automations. Talks to the Cursor Automations API over Connect RPC.",
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

	client, err := newAPIClient(endpoint, token, p.version)
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
	}
}

func (p *cursorProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewPlatformWorkflowDataSource,
	}
}

func getStringValue(value types.String, envKey string) string {
	if !value.IsNull() && !value.IsUnknown() {
		return strings.TrimSpace(value.ValueString())
	}
	return strings.TrimSpace(os.Getenv(envKey))
}
