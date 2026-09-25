package provider

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	maxOriginInboundIPAllowlistEntries        = 100
	maxOriginInboundIPAllowlistDescriptionLen = 255
)

var (
	_ resource.Resource                   = (*originInboundIPAllowlistEntryResource)(nil)
	_ resource.ResourceWithImportState    = (*originInboundIPAllowlistEntryResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*originInboundIPAllowlistEntryResource)(nil)
	_ resource.ResourceWithValidateConfig = (*originInboundIPAllowlistEntryResource)(nil)
)

type originInboundIPAllowlistEntryResource struct {
	client *apiClient
}

type originInboundIPAllowlistEntryModel struct {
	ID                 types.String `tfsdk:"id"`
	Namespace          types.String `tfsdk:"namespace"`
	CIDR               types.String `tfsdk:"cidr"`
	Description        types.String `tfsdk:"description"`
	Enabled            types.Bool   `tfsdk:"enabled"`
	CreatedAt          types.String `tfsdk:"created_at"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
}

func (m originInboundIPAllowlistEntryModel) withEntry(entry originInboundIPAllowlistEntry) originInboundIPAllowlistEntryModel {
	m.ID = types.StringValue(entry.ID)
	m.CIDR = types.StringValue(entry.CIDR)
	m.Description = types.StringValue(entry.Description)
	m.Enabled = types.BoolValue(entry.Enabled)
	m.CreatedAt = types.StringValue(entry.CreatedAt)
	m.DeletionProtection = boolOrDefault(m.DeletionProtection, true)
	return m
}

func NewOriginInboundIPAllowlistEntryResource() resource.Resource {
	return &originInboundIPAllowlistEntryResource{}
}

func (r *originInboundIPAllowlistEntryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_inbound_ip_allowlist_entry"
}

func (r *originInboundIPAllowlistEntryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: fmt.Sprintf("Manages one entry on an Origin namespace's inbound IP allowlist. While cursor_origin_inbound_ip_allowlist enforces the list and at least one entry is enabled, only the addresses of enabled entries can reach the namespace's repositories. Changing cidr, description, or enabled updates the entry in place and keeps its ID. A namespace lists at most %d entries and cannot list the same cidr spelling twice. While the list is enforced, the Origin API rejects a change or removal that would exclude the caller's own address. deletion_protection defaults to true, so Terraform will not remove or replace the entry until that is set to false and applied.", maxOriginInboundIPAllowlistEntries),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Origin-assigned entry ID (nsip_...). Stays the same when cidr changes.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"namespace": schema.StringAttribute{
				Required:    true,
				Description: "Slug of the team namespace whose allowlist holds the entry. Changing this replaces the entry. Blocked while deletion_protection is true.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cidr": schema.StringAttribute{
				Required:    true,
				Description: "IPv4 or IPv6 address or CIDR range, for example 203.0.113.0/24, 203.0.113.7, or 2001:db8::/32. Stored as spelled; a range covering an entire address space (/0) is rejected. Changing it updates the entry in place and keeps its ID.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: fmt.Sprintf("Label for the entry, at most %d characters. An empty string clears it.", maxOriginInboundIPAllowlistDescriptionLen),
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the entry admits its addresses. A disabled entry stays listed but admits nothing. Defaults to true.",
			},
			"created_at": schema.StringAttribute{
				Computed:    true,
				Description: "RFC 3339 time the entry was added.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"deletion_protection": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "When true, Terraform will not remove this entry. That includes terraform destroy and replacements caused by changing namespace. Set to false and apply before destroying or moving the entry. Null is treated as protected.",
			},
		},
	}
}

func (r *originInboundIPAllowlistEntryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *originInboundIPAllowlistEntryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan originInboundIPAllowlistEntryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if err := validateOriginInboundIPAllowlistEntry(plan); err != nil {
		resp.Diagnostics.AddError("Invalid Origin inbound IP allowlist entry", err.Error())
		return
	}
	if plan.Namespace.IsUnknown() || plan.CIDR.IsUnknown() || plan.Description.IsUnknown() || plan.Enabled.IsUnknown() {
		resp.Diagnostics.AddError("Invalid Origin inbound IP allowlist entry", "entry configuration is incomplete")
		return
	}
	entry, err := r.client.addOriginInboundIPAllowlistEntry(ctx, plan.Namespace.ValueString(), originInboundIPAllowlistEntryAdd{
		CIDR:        plan.CIDR.ValueString(),
		Description: plan.Description.ValueString(),
		Enabled:     plan.Enabled.ValueBool(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to add Origin inbound IP allowlist entry", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan.withEntry(*entry))...)
}

func (r *originInboundIPAllowlistEntryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state originInboundIPAllowlistEntryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	entry, err := r.client.lookupOriginInboundIPAllowlistEntry(ctx, state.Namespace.ValueString(), state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Origin inbound IP allowlist entry", err.Error())
		return
	}
	if entry == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state.withEntry(*entry))...)
}

func (r *originInboundIPAllowlistEntryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state originInboundIPAllowlistEntryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if err := validateOriginInboundIPAllowlistEntry(plan); err != nil {
		resp.Diagnostics.AddError("Invalid Origin inbound IP allowlist entry", err.Error())
		return
	}
	patch := inboundIPAllowlistEntryPatch(state, plan)
	if patch.empty() {
		plan.ID, plan.CreatedAt = state.ID, state.CreatedAt
		plan.DeletionProtection = boolOrDefault(plan.DeletionProtection, true)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	entry, err := r.client.updateOriginInboundIPAllowlistEntry(ctx, state.Namespace.ValueString(), state.ID.ValueString(), patch)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update Origin inbound IP allowlist entry", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan.withEntry(*entry))...)
}

func inboundIPAllowlistEntryPatch(state, plan originInboundIPAllowlistEntryModel) originInboundIPAllowlistEntryPatch {
	var patch originInboundIPAllowlistEntryPatch
	if !plan.CIDR.Equal(state.CIDR) {
		cidr := plan.CIDR.ValueString()
		patch.CIDR = &cidr
	}
	if !plan.Description.Equal(state.Description) {
		description := plan.Description.ValueString()
		patch.Description = &description
	}
	if !plan.Enabled.Equal(state.Enabled) {
		enabled := plan.Enabled.ValueBool()
		patch.Enabled = &enabled
	}
	return patch
}

func (r *originInboundIPAllowlistEntryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state originInboundIPAllowlistEntryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if err := refuseInboundIPAllowlistEntryDelete(state); err != nil {
		resp.Diagnostics.AddError("Origin inbound IP allowlist entry is protected from deletion", err.Error())
		return
	}
	err := r.client.deleteOriginInboundIPAllowlistEntry(ctx, state.Namespace.ValueString(), state.ID.ValueString())
	if err != nil && !isOriginNotFound(err) {
		resp.Diagnostics.AddError("Failed to remove Origin inbound IP allowlist entry", err.Error())
	}
}

func (r *originInboundIPAllowlistEntryResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		return
	}
	var state originInboundIPAllowlistEntryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if req.Plan.Raw.IsNull() {
		if err := refuseInboundIPAllowlistEntryDelete(state); err != nil {
			resp.Diagnostics.AddError("Origin inbound IP allowlist entry is protected from deletion", err.Error())
		}
		return
	}
	var plan originInboundIPAllowlistEntryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !deletionProtectionEnabled(state.DeletionProtection) {
		return
	}
	if plan.Namespace.IsUnknown() {
		resp.Diagnostics.AddError("Origin inbound IP allowlist entry is protected from replacement", "namespace is not known until apply, so this plan would replace the entry; set deletion_protection = false and apply first, or make namespace known at plan time")
		return
	}
	if knownStringChanged(state.Namespace, plan.Namespace) {
		resp.Diagnostics.AddError("Origin inbound IP allowlist entry is protected from replacement", "changing namespace removes the entry from the existing namespace; set deletion_protection = false and apply before moving it")
	}
}

func refuseInboundIPAllowlistEntryDelete(state originInboundIPAllowlistEntryModel) error {
	if deletionProtectionEnabled(state.DeletionProtection) {
		return fmt.Errorf("deletion_protection is enabled; set deletion_protection = false and apply before destroying or replacing this entry")
	}
	return nil
}

func (r *originInboundIPAllowlistEntryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	namespace, value, err := parseOriginInboundIPAllowlistEntryImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	id := value
	if isInboundIPAllowlistCIDR(value) {
		list, err := r.client.getOriginInboundIPAllowlist(ctx, namespace)
		if err != nil {
			resp.Diagnostics.AddError("Failed to import Origin inbound IP allowlist entry", err.Error())
			return
		}
		id = ""
		for _, entry := range list.Entries {
			if entry.CIDR == value {
				id = entry.ID
				break
			}
		}
		if id == "" {
			resp.Diagnostics.AddError("Failed to import Origin inbound IP allowlist entry", fmt.Sprintf("namespace %s has no inbound IP allowlist entry with cidr %s", namespace, value))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("namespace"), namespace)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
}

func (r *originInboundIPAllowlistEntryResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config originInboundIPAllowlistEntryModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateOriginInboundIPAllowlistEntry(config); err != nil {
		resp.Diagnostics.AddError("Invalid Origin inbound IP allowlist entry", err.Error())
	}
}

func parseOriginInboundIPAllowlistEntryImportID(id string) (string, string, error) {
	namespace, value, _ := strings.Cut(id, ":")
	if requireIDSafeSlug(types.StringValue(namespace), "namespace") != nil || value == "" || strings.TrimSpace(value) != value {
		return "", "", fmt.Errorf("invalid import ID %q, expected namespace:entry_id or namespace:<cidr>", id)
	}
	return namespace, value, nil
}

func validateOriginInboundIPAllowlistEntry(model originInboundIPAllowlistEntryModel) error {
	if err := requireIDSafeSlug(model.Namespace, "namespace"); err != nil {
		return err
	}
	if err := validateInboundIPAllowlistCIDR(model.CIDR); err != nil {
		return err
	}
	if model.Description.IsNull() || model.Description.IsUnknown() {
		return nil
	}
	description := model.Description.ValueString()
	if strings.TrimSpace(description) != description {
		return fmt.Errorf("description must not have leading or trailing spaces")
	}
	if utf8.RuneCountInString(description) > maxOriginInboundIPAllowlistDescriptionLen {
		return fmt.Errorf("description must be at most %d characters", maxOriginInboundIPAllowlistDescriptionLen)
	}
	return nil
}

func validateInboundIPAllowlistCIDR(value types.String) error {
	if err := requireNonEmptyUnpadded(value, "cidr"); err != nil {
		return err
	}
	if value.IsUnknown() {
		return nil
	}
	cidr := value.ValueString()
	if !isInboundIPAllowlistCIDR(cidr) {
		return fmt.Errorf("cidr %q must be an IPv4 or IPv6 address or CIDR range, for example 203.0.113.0/24, 203.0.113.7, or 2001:db8::/32", cidr)
	}
	if prefix, err := netip.ParsePrefix(cidr); err == nil && prefix.Bits() == 0 {
		return fmt.Errorf("cidr %q covers an entire address space; the Origin API rejects /0 ranges", cidr)
	}
	return nil
}

func isInboundIPAllowlistCIDR(value string) bool {
	if _, err := netip.ParsePrefix(value); err == nil {
		return true
	}
	addr, err := netip.ParseAddr(value)
	return err == nil && addr.Zone() == ""
}
