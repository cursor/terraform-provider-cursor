package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*originInboundIPAllowlistResource)(nil)
	_ resource.ResourceWithImportState    = (*originInboundIPAllowlistResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*originInboundIPAllowlistResource)(nil)
	_ resource.ResourceWithValidateConfig = (*originInboundIPAllowlistResource)(nil)
)

type originInboundIPAllowlistResource struct {
	client *apiClient
}

type originInboundIPAllowlistModel struct {
	ID                 types.String `tfsdk:"id"`
	Namespace          types.String `tfsdk:"namespace"`
	Enabled            types.Bool   `tfsdk:"enabled"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
}

func NewOriginInboundIPAllowlistResource() resource.Resource {
	return &originInboundIPAllowlistResource{}
}

func (r *originInboundIPAllowlistResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_inbound_ip_allowlist"
}

func (r *originInboundIPAllowlistResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages whether an Origin namespace enforces its inbound IP allowlist. While enforced and at least one entry is enabled, git over SSH and HTTPS, the API, and file downloads on the namespace's repositories accept requests only from listed addresses. Enabling a list that excludes the caller's own address is rejected, so add a cursor_origin_inbound_ip_allowlist_entry that admits it and reference it with depends_on; that also orders destroy so enforcement stops before the entries are removed. Allowlists are available on team namespaces that have the feature enabled. One resource per namespace. Destroying it stops enforcement; deletion_protection defaults to true, so Terraform will not destroy or replace it until that is set to false and applied.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The namespace slug.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"namespace": schema.StringAttribute{
				Required:    true,
				Description: "Slug of the team namespace. Changing this replaces the resource, which stops enforcement on the old namespace. Blocked while deletion_protection is true.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"enabled": schema.BoolAttribute{
				Required:    true,
				Description: "True to enforce the namespace's inbound IP allowlist, false to stop enforcing it.",
			},
			"deletion_protection": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "When true, Terraform will not destroy this resource, which would stop enforcing the allowlist. That includes terraform destroy and replacements caused by changing namespace. Set to false and apply before destroying or moving it. Null is treated as protected.",
			},
		},
	}
}

func (r *originInboundIPAllowlistResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*apiClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *apiClient, got %T", req.ProviderData))
		return
	}
	r.client = client
}

func (r *originInboundIPAllowlistResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan originInboundIPAllowlistModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	state, err := r.set(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update Origin inbound IP allowlist", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originInboundIPAllowlistResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state originInboundIPAllowlistModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	list, err := r.client.getOriginInboundIPAllowlist(ctx, state.Namespace.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Origin inbound IP allowlist", err.Error())
		return
	}
	state.ID = state.Namespace
	state.Enabled = types.BoolValue(list.Enabled)
	state.DeletionProtection = boolOrDefault(state.DeletionProtection, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originInboundIPAllowlistResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior originInboundIPAllowlistModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if !plan.Enabled.IsUnknown() && !prior.Enabled.IsNull() && plan.Enabled.Equal(prior.Enabled) {
		plan.ID = plan.Namespace
		plan.DeletionProtection = boolOrDefault(plan.DeletionProtection, true)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	state, err := r.set(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update Origin inbound IP allowlist", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originInboundIPAllowlistResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state originInboundIPAllowlistModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if err := refuseInboundIPAllowlistDelete(state); err != nil {
		resp.Diagnostics.AddError("Origin inbound IP allowlist is protected from deletion", err.Error())
		return
	}
	_, err := r.client.setOriginInboundIPAllowlistEnabled(ctx, state.Namespace.ValueString(), false)
	if err != nil && !isOriginNotFound(err) {
		resp.Diagnostics.AddError("Failed to stop enforcing Origin inbound IP allowlist", err.Error())
	}
}

func (r *originInboundIPAllowlistResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		return
	}
	var state originInboundIPAllowlistModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if req.Plan.Raw.IsNull() {
		if err := refuseInboundIPAllowlistDelete(state); err != nil {
			resp.Diagnostics.AddError("Origin inbound IP allowlist is protected from deletion", err.Error())
		}
		return
	}
	var plan originInboundIPAllowlistModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !deletionProtectionEnabled(state.DeletionProtection) {
		return
	}
	if plan.Namespace.IsUnknown() {
		resp.Diagnostics.AddError("Origin inbound IP allowlist is protected from replacement", "namespace is not known until apply, so this plan would replace the allowlist and stop enforcement on the existing namespace; set deletion_protection = false and apply first, or make namespace known at plan time")
		return
	}
	if knownStringChanged(state.Namespace, plan.Namespace) {
		resp.Diagnostics.AddError("Origin inbound IP allowlist is protected from replacement", "changing namespace stops enforcement on the existing namespace; set deletion_protection = false and apply before moving it")
	}
}

func refuseInboundIPAllowlistDelete(state originInboundIPAllowlistModel) error {
	if deletionProtectionEnabled(state.DeletionProtection) {
		return fmt.Errorf("deletion_protection is enabled; set deletion_protection = false and apply before destroying or replacing this allowlist")
	}
	return nil
}

func (r *originInboundIPAllowlistResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if err := requireIDSafeSlug(types.StringValue(req.ID), "namespace"); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("invalid import ID %q, expected the namespace slug: %s", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("namespace"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
}

func (r *originInboundIPAllowlistResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config originInboundIPAllowlistModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := requireIDSafeSlug(config.Namespace, "namespace"); err != nil {
		resp.Diagnostics.AddError("Invalid Origin inbound IP allowlist", err.Error())
	}
}

func (r *originInboundIPAllowlistResource) set(ctx context.Context, plan originInboundIPAllowlistModel) (originInboundIPAllowlistModel, error) {
	if err := requireIDSafeSlug(plan.Namespace, "namespace"); err != nil {
		return plan, err
	}
	if plan.Namespace.IsUnknown() || plan.Enabled.IsUnknown() || plan.Enabled.IsNull() {
		return plan, fmt.Errorf("allowlist configuration is incomplete")
	}
	list, err := r.client.setOriginInboundIPAllowlistEnabled(ctx, plan.Namespace.ValueString(), plan.Enabled.ValueBool())
	if err != nil {
		return plan, err
	}
	plan.ID = plan.Namespace
	plan.Enabled = types.BoolValue(list.Enabled)
	plan.DeletionProtection = boolOrDefault(plan.DeletionProtection, true)
	return plan, nil
}
