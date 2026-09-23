package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*originSSHCertificateAuthorityResource)(nil)
	_ resource.ResourceWithImportState    = (*originSSHCertificateAuthorityResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*originSSHCertificateAuthorityResource)(nil)
	_ resource.ResourceWithValidateConfig = (*originSSHCertificateAuthorityResource)(nil)
)

type originSSHCertificateAuthorityResource struct {
	client *apiClient
}

type originSSHCertificateAuthorityModel struct {
	ID                 types.String `tfsdk:"id"`
	Owner              types.String `tfsdk:"owner"`
	Name               types.String `tfsdk:"name"`
	PublicKey          types.String `tfsdk:"public_key"`
	KeyType            types.String `tfsdk:"key_type"`
	Fingerprint        types.String `tfsdk:"fingerprint"`
	CreatedAt          types.String `tfsdk:"created_at"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
}

// withAuthority copies the server-side view; public_key keeps the configured line unless the key itself differs.
func (m originSSHCertificateAuthorityModel) withAuthority(authority originSSHCertificateAuthority) originSSHCertificateAuthorityModel {
	m.ID = types.StringValue(authority.ID)
	m.Name = types.StringValue(authority.Name)
	m.KeyType = types.StringValue(authority.KeyType)
	m.Fingerprint = types.StringValue(authority.Fingerprint)
	m.CreatedAt = types.StringValue(authority.CreatedAt)
	m.DeletionProtection = boolOrDefault(m.DeletionProtection, true)
	if normalizeSSHPublicKey(m.PublicKey.ValueString()) != normalizeSSHPublicKey(authority.PublicKey) {
		m.PublicKey = types.StringValue(authority.PublicKey)
	}
	return m
}

func NewOriginSSHCertificateAuthorityResource() resource.Resource {
	return &originSSHCertificateAuthorityResource{}
}

func (r *originSSHCertificateAuthorityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_ssh_certificate_authority"
}

func (r *originSSHCertificateAuthorityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages one SSH certificate authority an Origin owner trusts: members of the owning team can use git over SSH on the owner's repositories with user certificates the authority signed. The Origin API has no update for an authority: changing owner or the key itself replaces it, name is fixed once the authority is added, and removing an authority invalidates every certificate it signed. deletion_protection defaults to true, so Terraform will not remove or replace the authority until that is set to false and applied. Rotate a key with lifecycle { create_before_destroy = true } so the new authority exists before the old one is removed; while the owner requires certificates its last authority cannot be removed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Origin-assigned authority ID (nsca_...).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"owner": schema.StringAttribute{
				Required:    true,
				Description: "Owner slug of the team namespace that trusts the authority. Changing this replaces the authority. Blocked while deletion_protection is true.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: fmt.Sprintf("Label for the authority, at most %d characters. Set when the authority is added; the Origin API has no rename, so a later change is rejected at plan time (terraform apply -replace included). To relabel, remove the authority and add it again with the new name, which destroys it first; to accept renames made elsewhere, use lifecycle { ignore_changes = [name] }.", maxOriginSSHAuthorityNameLen),
			},
			"public_key": schema.StringAttribute{
				Required:    true,
				Description: "The authority's public key as one OpenSSH authorized_keys line: <key_type> <base64> [comment]. Accepted key types are ssh-ed25519, ecdsa-sha2-nistp256, ecdsa-sha2-nistp384, ecdsa-sha2-nistp521, and ssh-rsa with at least 2048 bits; certificates are rejected. Changing the key replaces the authority, which is blocked while deletion_protection is true; changing only the comment or whitespace does not.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIf(sshPublicKeyChanged, "Replaces the authority when the key itself changes.", "Replaces the authority when the key itself changes."),
				},
			},
			"deletion_protection": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "When true, Terraform will not remove this authority. That includes terraform destroy and replacements caused by changing owner or the key. Set to false and apply before destroying or rotating the authority. Null is treated as protected.",
			},
			"key_type": schema.StringAttribute{
				Computed:    true,
				Description: "OpenSSH key type of the public key, for example ssh-ed25519.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"fingerprint": schema.StringAttribute{
				Computed:    true,
				Description: "SHA-256 fingerprint of the public key as SHA256:<base64>, the form ssh-keygen -l prints.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Computed:    true,
				Description: "RFC 3339 time the authority was added.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *originSSHCertificateAuthorityResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *originSSHCertificateAuthorityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan originSSHCertificateAuthorityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if err := validateOriginSSHCertificateAuthority(plan); err != nil {
		resp.Diagnostics.AddError("Invalid Origin SSH certificate authority", err.Error())
		return
	}
	if plan.Owner.IsUnknown() || plan.Name.IsUnknown() || plan.PublicKey.IsUnknown() {
		resp.Diagnostics.AddError("Invalid Origin SSH certificate authority", "authority configuration is incomplete")
		return
	}
	authority, err := r.client.addOriginSSHCertificateAuthority(ctx, plan.Owner.ValueString(), originSSHCertificateAuthorityWrite{
		PublicKey: strings.TrimSpace(plan.PublicKey.ValueString()),
		Name:      plan.Name.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to add Origin SSH certificate authority", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan.withAuthority(*authority))...)
}

func (r *originSSHCertificateAuthorityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state originSSHCertificateAuthorityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	list, err := r.client.listOriginSSHCertificateAuthorities(ctx, state.Owner.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Origin SSH certificate authority", err.Error())
		return
	}
	found := findOriginSSHCertificateAuthority(list.CertificateAuthorities, state.ID.ValueString())
	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state.withAuthority(*found))...)
}

// A rename is refused unless the authority is being replaced anyway: the API has no rename, and a
// same-key replace would have to destroy first, so it is left to an explicit remove and re-add.
func (r *originSSHCertificateAuthorityResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		return
	}
	if req.Plan.Raw.IsNull() {
		var state originSSHCertificateAuthorityModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if err := refuseSSHCertificateAuthorityDelete(state); err != nil {
			resp.Diagnostics.AddError("Origin SSH certificate authority is protected from deletion", err.Error())
		}
		return
	}
	var plan, state originSSHCertificateAuthorityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := sshCertificateAuthorityReplacementGuard(state, plan); err != nil {
		resp.Diagnostics.AddError("Origin SSH certificate authority is protected from replacement", err.Error())
		return
	}
	if plan.Owner.IsUnknown() || knownStringChanged(state.Owner, plan.Owner) || plan.PublicKey.IsUnknown() || normalizeSSHPublicKey(plan.PublicKey.ValueString()) != normalizeSSHPublicKey(state.PublicKey.ValueString()) {
		return
	}
	if err := renameError(plan, state); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Origin SSH certificate authority cannot be renamed", err.Error())
	}
}

func refuseSSHCertificateAuthorityDelete(state originSSHCertificateAuthorityModel) error {
	if deletionProtectionEnabled(state.DeletionProtection) {
		return fmt.Errorf("deletion_protection is enabled; set deletion_protection = false and apply before destroying or replacing this authority")
	}
	return nil
}

// An unknown owner or key is planned as a replace too, so it is refused while protected: with
// create_before_destroy the new authority would be added before the protected delete fails.
func sshCertificateAuthorityReplacementGuard(state, plan originSSHCertificateAuthorityModel) error {
	if !deletionProtectionEnabled(state.DeletionProtection) {
		return nil
	}
	if plan.Owner.IsUnknown() {
		return fmt.Errorf("owner is not known until apply, so this plan would replace the authority; set deletion_protection = false and apply first, or make owner known at plan time")
	}
	if knownStringChanged(state.Owner, plan.Owner) {
		return fmt.Errorf("changing owner removes the existing authority; set deletion_protection = false and apply before moving it")
	}
	if plan.PublicKey.IsUnknown() {
		return fmt.Errorf("public_key is not known until apply, so this plan would replace the authority; set deletion_protection = false and apply first, or make public_key known at plan time")
	}
	if !plan.PublicKey.IsNull() && !state.PublicKey.IsNull() && normalizeSSHPublicKey(plan.PublicKey.ValueString()) != normalizeSSHPublicKey(state.PublicKey.ValueString()) {
		return fmt.Errorf("changing the key removes the existing authority; set deletion_protection = false and apply before rotating it")
	}
	return nil
}

// Every change to the key replaces the authority, so an update only records a comment or whitespace edit.
func (r *originSSHCertificateAuthorityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state originSSHCertificateAuthorityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if normalizeSSHPublicKey(plan.PublicKey.ValueString()) != normalizeSSHPublicKey(state.PublicKey.ValueString()) {
		resp.Diagnostics.AddError("Failed to update Origin SSH certificate authority", "the public key changed; the Origin API has no update for an authority, so this change must replace it")
		return
	}
	if err := renameError(plan, state); err != nil {
		resp.Diagnostics.AddError("Failed to update Origin SSH certificate authority", err.Error())
		return
	}
	plan.ID, plan.KeyType, plan.Fingerprint, plan.CreatedAt = state.ID, state.KeyType, state.Fingerprint, state.CreatedAt
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func renameError(plan, state originSSHCertificateAuthorityModel) error {
	if plan.Name.IsUnknown() || plan.Name.IsNull() || state.Name.IsNull() || plan.Name.ValueString() == state.Name.ValueString() {
		return nil
	}
	return fmt.Errorf("the Origin API has no rename, so name stays %q for the life of the authority. Set name back to that value, add lifecycle { ignore_changes = [name] } to accept renames made elsewhere, or remove the authority and add it again with the new name; that destroys it first, since the same key cannot be listed twice, and the last authority cannot be removed while the owner requires certificates", state.Name.ValueString())
}

func (r *originSSHCertificateAuthorityResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state originSSHCertificateAuthorityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	if err := refuseSSHCertificateAuthorityDelete(state); err != nil {
		resp.Diagnostics.AddError("Origin SSH certificate authority is protected from deletion", err.Error())
		return
	}
	err := r.client.deleteOriginSSHCertificateAuthority(ctx, state.Owner.ValueString(), state.ID.ValueString())
	if err != nil && !isOriginNotFound(err) {
		resp.Diagnostics.AddError("Failed to remove Origin SSH certificate authority", err.Error())
	}
}

// Import IDs are owner:nsca_... or owner:SHA256:<fingerprint>; a fingerprint is resolved through the owner's list.
func (r *originSSHCertificateAuthorityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	owner, value, err := parseOriginSSHCertificateAuthorityImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	id := value
	if strings.HasPrefix(value, originSSHFingerprintPrefix) {
		list, err := r.client.listOriginSSHCertificateAuthorities(ctx, owner)
		if err != nil {
			resp.Diagnostics.AddError("Failed to import Origin SSH certificate authority", err.Error())
			return
		}
		id = ""
		for _, authority := range list.CertificateAuthorities {
			if authority.Fingerprint == value {
				id = authority.ID
				break
			}
		}
		if id == "" {
			resp.Diagnostics.AddError("Failed to import Origin SSH certificate authority", fmt.Sprintf("owner %s has no SSH certificate authority with fingerprint %s", owner, value))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner"), owner)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
}

func (r *originSSHCertificateAuthorityResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config originSSHCertificateAuthorityModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateOriginSSHCertificateAuthority(config); err != nil {
		resp.Diagnostics.AddError("Invalid Origin SSH certificate authority", err.Error())
	}
}

func sshPublicKeyChanged(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = req.PlanValue.IsUnknown() || normalizeSSHPublicKey(req.PlanValue.ValueString()) != normalizeSSHPublicKey(req.StateValue.ValueString())
}

func parseOriginSSHCertificateAuthorityImportID(id string) (string, string, error) {
	parts := strings.SplitN(id, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.TrimSpace(id) != id || strings.ContainsAny(parts[0], "/") {
		return "", "", fmt.Errorf("invalid import ID %q, expected owner:certificate_authority_id or owner:SHA256:<fingerprint>", id)
	}
	return parts[0], parts[1], nil
}

func validateOriginSSHCertificateAuthority(model originSSHCertificateAuthorityModel) error {
	if err := requireIDSafeSlug(model.Owner, "owner"); err != nil {
		return err
	}
	if err := requireNonEmptyUnpadded(model.Name, "name"); err != nil {
		return err
	}
	if !model.Name.IsUnknown() && len(model.Name.ValueString()) > maxOriginSSHAuthorityNameLen {
		return fmt.Errorf("name must be at most %d characters", maxOriginSSHAuthorityNameLen)
	}
	return validateSSHPublicKeyLine(model.PublicKey)
}

func validateSSHPublicKeyLine(value types.String) error {
	if value.IsUnknown() {
		return nil
	}
	if value.IsNull() {
		return fmt.Errorf("public_key is required")
	}
	line := strings.TrimSpace(value.ValueString())
	fields := strings.Fields(line)
	if len(fields) < 2 || strings.ContainsAny(line, "\r\n") {
		return fmt.Errorf("public_key must be one OpenSSH authorized_keys line: <key_type> <base64> [comment]")
	}
	if strings.HasSuffix(fields[0], originSSHCertificateKeySuffix) {
		return fmt.Errorf("public_key is an SSH certificate (%s); use the certificate authority's own public key", fields[0])
	}
	return nil
}
