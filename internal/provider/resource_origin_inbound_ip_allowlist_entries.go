package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*originInboundIPAllowlistEntriesResource)(nil)
	_ resource.ResourceWithImportState    = (*originInboundIPAllowlistEntriesResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*originInboundIPAllowlistEntriesResource)(nil)
	_ resource.ResourceWithValidateConfig = (*originInboundIPAllowlistEntriesResource)(nil)
)

var originInboundIPAllowlistEntriesEntryType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"id":          types.StringType,
	"cidr":        types.StringType,
	"description": types.StringType,
	"enabled":     types.BoolType,
}}

var originInboundIPAllowlistEntryViolation = regexp.MustCompile(`^entries\[(\d+)\]`)

type originInboundIPAllowlistEntriesResource struct {
	client *apiClient
}

type originInboundIPAllowlistEntriesModel struct {
	ID                 types.String `tfsdk:"id"`
	Namespace          types.String `tfsdk:"namespace"`
	Entry              types.Set    `tfsdk:"entry"`
	Etag               types.String `tfsdk:"etag"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
}

type originInboundIPAllowlistEntriesEntryModel struct {
	ID          types.String `tfsdk:"id"`
	CIDR        types.String `tfsdk:"cidr"`
	Description types.String `tfsdk:"description"`
	Enabled     types.Bool   `tfsdk:"enabled"`
}

func NewOriginInboundIPAllowlistEntriesResource() resource.Resource {
	return &originInboundIPAllowlistEntriesResource{}
}

func (r *originInboundIPAllowlistEntriesResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_inbound_ip_allowlist_entries"
}

func (r *originInboundIPAllowlistEntriesResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: fmt.Sprintf("Manages the complete set of entries on an Origin namespace's inbound IP allowlist with one ReplaceInboundIpAllowlistEntries call per create, update, or destroy, however many entries change. Entries not listed here are removed. Do not use this resource together with cursor_origin_inbound_ip_allowlist_entry for the same namespace; the two will overwrite each other's changes. Whether the list is enforced stays with cursor_origin_inbound_ip_allowlist. A namespace lists at most %d entries and cannot list the same cidr spelling twice. Origin rejects a set that would exclude the caller's own address from an enforced list, so keep an entry that admits the address Terraform calls from. Updates send the etag from the last read, so Origin rejects the replace if the entries changed since then. deletion_protection defaults to true, so Terraform will not remove every entry or move them to another namespace until that is set to false and applied.", maxOriginInboundIPAllowlistEntries),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The namespace slug.",
			},
			"namespace": schema.StringAttribute{
				Required:    true,
				Description: "Slug of the team namespace whose allowlist entries this resource owns. Changing this removes every entry from the old namespace and writes the set to the new one. Blocked while deletion_protection is true.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"etag": schema.StringAttribute{
				Computed:    true,
				Description: "Origin's etag for the namespace's entry set as of the last read or write. Sent with updates so a set changed outside Terraform is not overwritten.",
			},
			"deletion_protection": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "When true, Terraform will not destroy this resource, which would remove every entry on the namespace's allowlist. That includes terraform destroy and replacements caused by changing namespace. Set to false and apply before destroying or moving it. Null is treated as protected.",
			},
		},
		Blocks: map[string]schema.Block{
			"entry": schema.SetNestedBlock{
				Description: fmt.Sprintf("One entry on the allowlist. Omit every entry block to remove all entries. At most %d.", maxOriginInboundIPAllowlistEntries),
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:    true,
							Description: "Origin-assigned entry ID (nsip_...). Kept while the same cidr spelling stays listed.",
						},
						"cidr": schema.StringAttribute{
							Required:    true,
							Description: "IPv4 or IPv6 address or CIDR range, for example 203.0.113.0/24, 203.0.113.7, or 2001:db8::/32. Stored as spelled, so 10.0.0.1/8 and 10.0.0.0/8 are different entries; each spelling may appear once. A range covering an entire address space (/0) is rejected.",
						},
						"description": schema.StringAttribute{
							Optional:    true,
							Computed:    true,
							Description: fmt.Sprintf("Label for the entry, at most %d characters. Defaults to an empty string.", maxOriginInboundIPAllowlistDescriptionLen),
						},
						"enabled": schema.BoolAttribute{
							Optional:    true,
							Computed:    true,
							Description: "Whether the entry admits its addresses. A disabled entry stays listed but admits nothing. Defaults to true.",
						},
					},
				},
			},
		},
	}
}

func (r *originInboundIPAllowlistEntriesResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *originInboundIPAllowlistEntriesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan originInboundIPAllowlistEntriesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	state, diags := r.replace(ctx, plan, "")
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originInboundIPAllowlistEntriesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state originInboundIPAllowlistEntriesModel
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
		resp.Diagnostics.AddError("Failed to read Origin inbound IP allowlist entries", err.Error())
		return
	}
	state, diags := inboundEntriesFromAllowlist(ctx, state, list)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originInboundIPAllowlistEntriesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior originInboundIPAllowlistEntriesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	planned, diags := inboundEntriesFromSet(ctx, plan.Entry)
	resp.Diagnostics.Append(diags...)
	stored, diags := inboundEntriesFromSet(ctx, prior.Entry)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Entry.IsUnknown() && sameInboundEntries(planned, stored) {
		plan.ID, plan.Entry, plan.Etag = prior.ID, prior.Entry, prior.Etag
		plan.DeletionProtection = boolOrDefault(plan.DeletionProtection, true)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	state, diags := r.replace(ctx, plan, prior.Etag.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originInboundIPAllowlistEntriesResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state originInboundIPAllowlistEntriesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if err := refuseInboundIPAllowlistEntriesDelete(state); err != nil {
		resp.Diagnostics.AddError("Origin inbound IP allowlist entries are protected from deletion", err.Error())
		return
	}
	namespace := state.Namespace.ValueString()
	_, err := r.client.replaceOriginInboundIPAllowlistEntries(ctx, namespace, originInboundIPAllowlistReplace{AllowEmpty: true})
	if err != nil && !isOriginNotFound(err) {
		resp.Diagnostics.AddError("Failed to remove Origin inbound IP allowlist entries", inboundEntriesReplaceError(namespace, err, nil))
	}
}

func (r *originInboundIPAllowlistEntriesResource) replace(ctx context.Context, plan originInboundIPAllowlistEntriesModel, etag string) (originInboundIPAllowlistEntriesModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	if err := validateOriginInboundIPAllowlistEntries(ctx, plan); err != nil {
		diags.AddError("Invalid Origin inbound IP allowlist entries", err.Error())
		return plan, diags
	}
	planned, d := inboundEntriesFromSet(ctx, plan.Entry)
	diags.Append(d...)
	if diags.HasError() {
		return plan, diags
	}
	if plan.Namespace.IsUnknown() || plan.Entry.IsUnknown() || !inboundEntriesKnown(planned) {
		diags.AddError("Invalid Origin inbound IP allowlist entries", "entry configuration is incomplete")
		return plan, diags
	}
	adds := inboundEntriesReplaceRequest(planned)
	namespace := plan.Namespace.ValueString()
	result, err := r.client.replaceOriginInboundIPAllowlistEntries(ctx, namespace, originInboundIPAllowlistReplace{
		Entries:    adds,
		Etag:       etag,
		AllowEmpty: len(adds) == 0,
	})
	if err != nil {
		diags.AddError("Failed to replace Origin inbound IP allowlist entries", inboundEntriesReplaceError(namespace, err, adds))
		return plan, diags
	}
	return inboundEntriesFromAllowlist(ctx, plan, result.Allowlist)
}

// Planned entries are built from config because framework defaults inside set blocks cannot match config elements once ids are set.
func (r *originInboundIPAllowlistEntriesResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	var state originInboundIPAllowlistEntriesModel
	exists := !req.State.Raw.IsNull()
	if exists {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if req.Plan.Raw.IsNull() {
		if exists {
			if err := refuseInboundIPAllowlistEntriesDelete(state); err != nil {
				resp.Diagnostics.AddError("Origin inbound IP allowlist entries are protected from deletion", err.Error())
			}
		}
		return
	}
	var plan, config originInboundIPAllowlistEntriesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if exists && deletionProtectionEnabled(state.DeletionProtection) {
		if plan.Namespace.IsUnknown() {
			resp.Diagnostics.AddError("Origin inbound IP allowlist entries are protected from replacement", "namespace is not known until apply, so this plan would remove every entry from the existing namespace; set deletion_protection = false and apply first, or make namespace known at plan time")
			return
		}
		if knownStringChanged(state.Namespace, plan.Namespace) {
			resp.Diagnostics.AddError("Origin inbound IP allowlist entries are protected from replacement", "changing namespace removes every entry from the existing namespace; set deletion_protection = false and apply before moving them")
			return
		}
	}

	plan.ID = plan.Namespace
	plan.Etag = types.StringUnknown()
	if config.Entry.IsUnknown() {
		plan.Entry = types.SetUnknown(originInboundIPAllowlistEntriesEntryType)
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}
	configured, diags := inboundEntriesFromSet(ctx, config.Entry)
	resp.Diagnostics.Append(diags...)
	stored, diags := inboundEntriesFromSet(ctx, state.Entry)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	replacing := !exists || !plan.Namespace.Equal(state.Namespace)
	byCIDR := inboundEntriesByCIDR(stored)
	planned := make([]originInboundIPAllowlistEntriesEntryModel, 0, len(configured))
	for _, entry := range configured {
		if entry.Description.IsNull() {
			entry.Description = types.StringValue("")
		}
		if entry.Enabled.IsNull() {
			entry.Enabled = types.BoolValue(true)
		}
		entry.ID = types.StringUnknown()
		if prior, ok := byCIDR[entry.CIDR.ValueString()]; ok && !replacing && !entry.CIDR.IsUnknown() {
			entry.ID = prior.ID
		}
		planned = append(planned, entry)
	}
	if !replacing && inboundEntriesKnown(planned) && sameInboundEntries(planned, stored) {
		plan.Entry, plan.Etag = state.Entry, state.Etag
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}
	plan.Entry, diags = types.SetValueFrom(ctx, originInboundIPAllowlistEntriesEntryType, planned)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func refuseInboundIPAllowlistEntriesDelete(state originInboundIPAllowlistEntriesModel) error {
	if deletionProtectionEnabled(state.DeletionProtection) {
		return fmt.Errorf("deletion_protection is enabled; set deletion_protection = false and apply before destroying or replacing this resource, which removes every entry on the namespace's allowlist")
	}
	return nil
}

func (r *originInboundIPAllowlistEntriesResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if err := requireIDSafeSlug(types.StringValue(req.ID), "namespace"); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("invalid import ID %q, expected the namespace slug: %s", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("namespace"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
}

func (r *originInboundIPAllowlistEntriesResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config originInboundIPAllowlistEntriesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateOriginInboundIPAllowlistEntries(ctx, config); err != nil {
		resp.Diagnostics.AddError("Invalid Origin inbound IP allowlist entries", err.Error())
	}
}

func validateOriginInboundIPAllowlistEntries(ctx context.Context, model originInboundIPAllowlistEntriesModel) error {
	if err := requireIDSafeSlug(model.Namespace, "namespace"); err != nil {
		return err
	}
	if model.Entry.IsUnknown() {
		return nil
	}
	entries, diags := inboundEntriesFromSet(ctx, model.Entry)
	if diags.HasError() {
		return fmt.Errorf("reading entry blocks: %v", diags)
	}
	if len(entries) > maxOriginInboundIPAllowlistEntries {
		return fmt.Errorf("a namespace lists at most %d inbound IP allowlist entries, got %d", maxOriginInboundIPAllowlistEntries, len(entries))
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if err := validateInboundIPAllowlistCIDR(entry.CIDR); err != nil {
			return err
		}
		if err := validateInboundIPAllowlistDescription(entry.Description); err != nil {
			return fmt.Errorf("entry %s: %w", entry.CIDR.ValueString(), err)
		}
		if entry.CIDR.IsUnknown() {
			continue
		}
		cidr := entry.CIDR.ValueString()
		if seen[cidr] {
			return fmt.Errorf("cidr %q is listed more than once", cidr)
		}
		seen[cidr] = true
	}
	return nil
}

func inboundEntriesFromSet(ctx context.Context, set types.Set) ([]originInboundIPAllowlistEntriesEntryModel, diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return nil, nil
	}
	var entries []originInboundIPAllowlistEntriesEntryModel
	diags := set.ElementsAs(ctx, &entries, false)
	return entries, diags
}

func inboundEntriesKnown(entries []originInboundIPAllowlistEntriesEntryModel) bool {
	for _, entry := range entries {
		if entry.CIDR.IsUnknown() || entry.Description.IsUnknown() || entry.Enabled.IsUnknown() {
			return false
		}
	}
	return true
}

func sameInboundEntries(a, b []originInboundIPAllowlistEntriesEntryModel) bool {
	if len(a) != len(b) {
		return false
	}
	byCIDR := make(map[string]originInboundIPAllowlistEntriesEntryModel, len(b))
	for _, entry := range b {
		byCIDR[entry.CIDR.ValueString()] = entry
	}
	for _, entry := range a {
		other, ok := byCIDR[entry.CIDR.ValueString()]
		if !ok || !entry.CIDR.Equal(other.CIDR) || !entry.Description.Equal(other.Description) || !entry.Enabled.Equal(other.Enabled) {
			return false
		}
	}
	return true
}

func inboundEntriesByCIDR(entries []originInboundIPAllowlistEntriesEntryModel) map[string]originInboundIPAllowlistEntriesEntryModel {
	out := make(map[string]originInboundIPAllowlistEntriesEntryModel, len(entries))
	for _, entry := range entries {
		if !entry.CIDR.IsUnknown() && !entry.CIDR.IsNull() {
			out[entry.CIDR.ValueString()] = entry
		}
	}
	return out
}

func inboundEntriesReplaceRequest(entries []originInboundIPAllowlistEntriesEntryModel) []originInboundIPAllowlistEntryAdd {
	adds := make([]originInboundIPAllowlistEntryAdd, 0, len(entries))
	for _, entry := range entries {
		adds = append(adds, originInboundIPAllowlistEntryAdd{
			CIDR:        entry.CIDR.ValueString(),
			Description: entry.Description.ValueString(),
			Enabled:     boolOrDefault(entry.Enabled, true).ValueBool(),
		})
	}
	sort.Slice(adds, func(i, j int) bool { return adds[i].CIDR < adds[j].CIDR })
	return adds
}

func inboundEntriesFromAllowlist(ctx context.Context, model originInboundIPAllowlistEntriesModel, list *originInboundIPAllowlist) (originInboundIPAllowlistEntriesModel, diag.Diagnostics) {
	entries := make([]originInboundIPAllowlistEntriesEntryModel, 0, len(list.Entries))
	for _, entry := range list.Entries {
		entries = append(entries, originInboundIPAllowlistEntriesEntryModel{
			ID:          types.StringValue(entry.ID),
			CIDR:        types.StringValue(entry.CIDR),
			Description: types.StringValue(entry.Description),
			Enabled:     types.BoolValue(entry.Enabled),
		})
	}
	set, diags := types.SetValueFrom(ctx, originInboundIPAllowlistEntriesEntryType, entries)
	model.ID = model.Namespace
	model.Entry = set
	model.Etag = types.StringValue(list.Etag)
	model.DeletionProtection = boolOrDefault(model.DeletionProtection, true)
	return model, diags
}

func inboundEntriesReplaceError(namespace string, err error, adds []originInboundIPAllowlistEntryAdd) string {
	var apiErr *originAPIError
	if !errors.As(err, &apiErr) {
		return err.Error()
	}
	if apiErr.StatusCode == http.StatusConflict {
		return fmt.Sprintf("The inbound IP allowlist entries on namespace %s changed after Terraform last read them, so Origin rejected the replace. Run terraform apply -refresh-only (or terraform plan, which refreshes) to review the current entries, then apply again.\n\n%s", namespace, err)
	}
	msg := err.Error()
	for _, violation := range apiErr.FieldViolations {
		field := violation.Field
		if m := originInboundIPAllowlistEntryViolation.FindStringSubmatch(field); m != nil {
			if i, convErr := strconv.Atoi(m[1]); convErr == nil && i < len(adds) {
				field += " (cidr " + adds[i].CIDR + ")"
			}
		}
		msg += "\n" + strings.TrimSpace(field+": "+violation.Description)
	}
	return msg
}
