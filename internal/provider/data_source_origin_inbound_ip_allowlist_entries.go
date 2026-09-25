package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type originInboundIPAllowlistEntriesDataSource struct {
	client *apiClient
}

type originInboundIPAllowlistEntriesDataSourceModel struct {
	Namespace types.String                                `tfsdk:"namespace"`
	Enabled   types.Bool                                  `tfsdk:"enabled"`
	Entries   []originInboundIPAllowlistEntryElementModel `tfsdk:"entries"`
}

type originInboundIPAllowlistEntryElementModel struct {
	ID          types.String `tfsdk:"id"`
	CIDR        types.String `tfsdk:"cidr"`
	Description types.String `tfsdk:"description"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func NewOriginInboundIPAllowlistEntriesDataSource() datasource.DataSource {
	return &originInboundIPAllowlistEntriesDataSource{}
}

func (d *originInboundIPAllowlistEntriesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_inbound_ip_allowlist_entries"
}

func (d *originInboundIPAllowlistEntriesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: fmt.Sprintf("Lists the entries on an Origin namespace's inbound IP allowlist, oldest first, and whether the namespace enforces it. A namespace lists at most %d entries. Uses the provider auth token.", maxOriginInboundIPAllowlistEntries),
		Attributes: map[string]schema.Attribute{
			"namespace": schema.StringAttribute{
				Required:    true,
				Description: "Slug of the team namespace.",
			},
			"enabled": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the namespace enforces the allowlist. The list takes effect only while this is true and at least one entry is enabled.",
			},
			"entries": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every entry on the allowlist, oldest first.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:    true,
							Description: "Origin-assigned entry ID (nsip_...).",
						},
						"cidr": schema.StringAttribute{
							Computed:    true,
							Description: "IPv4 or IPv6 address or CIDR range, spelled as it was submitted.",
						},
						"description": schema.StringAttribute{
							Computed:    true,
							Description: "Label for the entry; empty when unset.",
						},
						"enabled": schema.BoolAttribute{
							Computed:    true,
							Description: "Whether the entry admits its addresses. A disabled entry stays listed but admits nothing.",
						},
						"created_at": schema.StringAttribute{
							Computed:    true,
							Description: "RFC 3339 time the entry was added.",
						},
					},
				},
			},
		},
	}
}

func (d *originInboundIPAllowlistEntriesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *originInboundIPAllowlistEntriesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config originInboundIPAllowlistEntriesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if err := requireIDSafeSlug(config.Namespace, "namespace"); err != nil {
		resp.Diagnostics.AddError("Invalid Origin namespace", err.Error())
		return
	}

	list, err := d.client.getOriginInboundIPAllowlist(ctx, config.Namespace.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Origin inbound IP allowlist", err.Error())
		return
	}
	config.Enabled = types.BoolValue(list.Enabled)
	config.Entries = make([]originInboundIPAllowlistEntryElementModel, 0, len(list.Entries))
	for _, entry := range list.Entries {
		config.Entries = append(config.Entries, originInboundIPAllowlistEntryElementModel{
			ID:          types.StringValue(entry.ID),
			CIDR:        types.StringValue(entry.CIDR),
			Description: types.StringValue(entry.Description),
			Enabled:     types.BoolValue(entry.Enabled),
			CreatedAt:   types.StringValue(entry.CreatedAt),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
