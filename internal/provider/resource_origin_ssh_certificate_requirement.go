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
	_ resource.Resource                   = (*originSSHCertificateRequirementResource)(nil)
	_ resource.ResourceWithImportState    = (*originSSHCertificateRequirementResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*originSSHCertificateRequirementResource)(nil)
	_ resource.ResourceWithUpgradeState   = (*originSSHCertificateRequirementResource)(nil)
	_ resource.ResourceWithValidateConfig = (*originSSHCertificateRequirementResource)(nil)
)

type originSSHCertificateRequirementResource struct {
	client *apiClient
}

type originSSHCertificateRequirementModel struct {
	ID                  types.String `tfsdk:"id"`
	Namespace           types.String `tfsdk:"namespace"`
	Owner               types.String `tfsdk:"owner"`
	RequireCertificates types.Bool   `tfsdk:"require_certificates"`
	DeletionProtection  types.Bool   `tfsdk:"deletion_protection"`
}

func NewOriginSSHCertificateRequirementResource() resource.Resource {
	return &originSSHCertificateRequirementResource{}
}

func (r *originSSHCertificateRequirementResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_ssh_certificate_requirement"
}

func (r *originSSHCertificateRequirementResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version:     1,
		Description: "Manages whether an Origin namespace requires SSH certificates. While required, git over SSH on the namespace's repositories accepts only certificates from the namespace's authorities: SSH keys registered by users and user API keys over HTTPS are refused. Requiring certificates needs at least one cursor_origin_ssh_certificate_authority on the namespace, so reference one (for example through namespace) or use depends_on; that also orders destroy so the flag is cleared before the last authority is removed. One resource per namespace. Destroying it sets the flag back to false; deletion_protection defaults to true, so Terraform will not destroy or replace it until that is set to false and applied.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The namespace slug.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"namespace": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Slug of the team namespace. Required unless the deprecated owner is set. Changing this replaces the resource, which clears the flag on the old namespace. Blocked while deletion_protection is true.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"owner": schema.StringAttribute{
				Optional:           true,
				Computed:           true,
				Description:        "Deprecated alias of namespace. Use namespace instead; if both are set they must match.",
				DeprecationMessage: "Use namespace instead.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"require_certificates": schema.BoolAttribute{
				Required:    true,
				Description: "True to require SSH certificates on the namespace's repositories.",
			},
			"deletion_protection": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "When true, Terraform will not destroy this resource, which would clear the flag. That includes terraform destroy and replacements caused by changing namespace. Set to false and apply before destroying or moving it. Null is treated as protected.",
			},
		},
	}
}

func (r *originSSHCertificateRequirementResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *originSSHCertificateRequirementResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan originSSHCertificateRequirementModel
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
		resp.Diagnostics.AddError("Failed to set Origin SSH certificate requirement", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originSSHCertificateRequirementResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state originSSHCertificateRequirementModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	list, err := r.client.listOriginSSHCertificateAuthorities(ctx, state.Namespace.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Origin SSH certificate requirement", err.Error())
		return
	}
	state.ID = state.Namespace
	state.RequireCertificates = types.BoolValue(list.RequireCertificates)
	state.DeletionProtection = boolOrDefault(state.DeletionProtection, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originSSHCertificateRequirementResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior originSSHCertificateRequirementModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	// Only deletion_protection changed: record it without re-sending the flag.
	if !plan.RequireCertificates.IsUnknown() && !prior.RequireCertificates.IsNull() && plan.RequireCertificates.Equal(prior.RequireCertificates) {
		plan.ID = plan.Namespace
		plan.DeletionProtection = boolOrDefault(plan.DeletionProtection, true)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	state, err := r.set(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to set Origin SSH certificate requirement", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originSSHCertificateRequirementResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state originSSHCertificateRequirementModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if err := refuseSSHCertificateRequirementDelete(state); err != nil {
		resp.Diagnostics.AddError("Origin SSH certificate requirement is protected from deletion", err.Error())
		return
	}
	_, err := r.client.setOriginSSHCertificateRequirement(ctx, state.Namespace.ValueString(), false)
	if err != nil && !isOriginNotFound(err) {
		resp.Diagnostics.AddError("Failed to clear Origin SSH certificate requirement", err.Error())
	}
}

func (r *originSSHCertificateRequirementResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		var state originSSHCertificateRequirementModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if err := refuseSSHCertificateRequirementDelete(state); err != nil {
			resp.Diagnostics.AddError("Origin SSH certificate requirement is protected from deletion", err.Error())
		}
		return
	}
	var plan, config originSSHCertificateRequirementModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	alignOriginNamespaceAlias(config.Namespace, config.Owner, &plan.Namespace, &plan.Owner)
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() || req.State.Raw.IsNull() {
		return
	}
	var state originSSHCertificateRequirementModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !deletionProtectionEnabled(state.DeletionProtection) {
		return
	}
	if plan.Namespace.IsUnknown() {
		resp.Diagnostics.AddError("Origin SSH certificate requirement is protected from replacement", "namespace is not known until apply, so this plan would replace the requirement and clear the flag on the existing namespace; set deletion_protection = false and apply first, or make namespace known at plan time")
		return
	}
	if knownStringChanged(state.Namespace, plan.Namespace) {
		resp.Diagnostics.AddError("Origin SSH certificate requirement is protected from replacement", "changing namespace clears the flag on the existing namespace; set deletion_protection = false and apply before moving it")
	}
}

func refuseSSHCertificateRequirementDelete(state originSSHCertificateRequirementModel) error {
	if deletionProtectionEnabled(state.DeletionProtection) {
		return fmt.Errorf("deletion_protection is enabled; set deletion_protection = false and apply before destroying or replacing this requirement")
	}
	return nil
}

func (r *originSSHCertificateRequirementResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if err := requireIDSafeSlug(types.StringValue(req.ID), "namespace"); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("invalid import ID %q, expected the namespace slug: %s", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("namespace"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
}

func (r *originSSHCertificateRequirementResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config originSSHCertificateRequirementModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateOriginNamespaceAlias(config.Namespace, config.Owner); err != nil {
		resp.Diagnostics.AddError("Invalid Origin SSH certificate requirement", err.Error())
	}
}

func (r *originSSHCertificateRequirementResource) set(ctx context.Context, plan originSSHCertificateRequirementModel) (originSSHCertificateRequirementModel, error) {
	if err := requireIDSafeSlug(plan.Namespace, "namespace"); err != nil {
		return plan, err
	}
	if plan.Namespace.IsUnknown() || plan.RequireCertificates.IsUnknown() || plan.RequireCertificates.IsNull() {
		return plan, fmt.Errorf("requirement configuration is incomplete")
	}
	require, err := r.client.setOriginSSHCertificateRequirement(ctx, plan.Namespace.ValueString(), plan.RequireCertificates.ValueBool())
	if err != nil {
		return plan, err
	}
	plan.ID = plan.Namespace
	plan.RequireCertificates = types.BoolValue(require)
	plan.DeletionProtection = boolOrDefault(plan.DeletionProtection, true)
	return plan, nil
}

type originSSHCertificateRequirementModelV0 struct {
	ID                  types.String `tfsdk:"id"`
	Owner               types.String `tfsdk:"owner"`
	RequireCertificates types.Bool   `tfsdk:"require_certificates"`
	DeletionProtection  types.Bool   `tfsdk:"deletion_protection"`
}

func (r *originSSHCertificateRequirementResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema: &schema.Schema{
				Attributes: map[string]schema.Attribute{
					"id":                   schema.StringAttribute{Computed: true},
					"owner":                schema.StringAttribute{Required: true},
					"require_certificates": schema.BoolAttribute{Required: true},
					"deletion_protection":  schema.BoolAttribute{Optional: true, Computed: true},
				},
			},
			StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
				var prior originSSHCertificateRequirementModelV0
				resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
				if resp.Diagnostics.HasError() {
					return
				}
				resp.Diagnostics.Append(resp.State.Set(ctx, originSSHCertificateRequirementModel{
					ID:                  prior.ID,
					Namespace:           prior.Owner,
					Owner:               prior.Owner,
					RequireCertificates: prior.RequireCertificates,
					DeletionProtection:  prior.DeletionProtection,
				})...)
			},
		},
	}
}
