package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*originOwnerGrantResource)(nil)
	_ resource.ResourceWithImportState    = (*originOwnerGrantResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*originOwnerGrantResource)(nil)
	_ resource.ResourceWithValidateConfig = (*originOwnerGrantResource)(nil)
)

var originOwnerPermissions = []string{originPermissionRead, originPermissionContributor, originPermissionWrite, originPermissionAdmin}

type originOwnerGrantResource struct {
	client *apiClient
}

type originOwnerGrantModel struct {
	ID         types.String `tfsdk:"id"`
	Owner      types.String `tfsdk:"owner"`
	Permission types.String `tfsdk:"permission"`
	UserEmail  types.String `tfsdk:"user_email"`
	GroupName  types.String `tfsdk:"group_name"`
	User       types.Object `tfsdk:"user"`
	Group      types.Object `tfsdk:"group"`
	TeamGroup  types.Object `tfsdk:"team_group"`
}

func (m originOwnerGrantModel) principal() originGrantPrincipal {
	return originGrantPrincipal{UserEmail: m.UserEmail, GroupName: m.GroupName, User: m.User, Group: m.Group, TeamGroup: m.TeamGroup}
}

func (m originOwnerGrantModel) withPrincipal(p originGrantPrincipal) originOwnerGrantModel {
	m.UserEmail, m.GroupName, m.User, m.Group, m.TeamGroup = p.UserEmail, p.GroupName, p.User, p.Group, p.TeamGroup
	return m
}

func (m originOwnerGrantModel) collection() string {
	return originOwnerGrantsPath(m.Owner.ValueString())
}

func NewOriginOwnerGrantResource() resource.Resource {
	return &originOwnerGrantResource{}
}

func (r *originOwnerGrantResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_owner_grant"
}

func (r *originOwnerGrantResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:    true,
			Description: "Composite ID owner:kind:principal built from the stored principal ID, for example acme:group:grp_01... or acme:team_group:admins.",
		},
		"owner": schema.StringAttribute{
			Required:    true,
			Description: "Owner slug: the team or user namespace the grant applies to. Changing this replaces the grant.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.RequiresReplace(),
			},
		},
		"permission": schema.StringAttribute{
			Required:    true,
			Description: fmt.Sprintf("Permission preset: read, contributor, write, or admin, sent to the API as %sREAD and so on. Refresh reports custom for a grant whose policy is not a public preset; custom cannot be written, so applying replaces that policy with the configured preset.", originOwnerPermissionPrefix),
		},
	}
	for name, attribute := range originGrantPrincipalSchema() {
		attributes[name] = attribute
	}
	resp.Schema = schema.Schema{
		Description: "Manages one direct grant on an Origin owner namespace: a permission preset for a user, a Cursor org group, or a team floor across every repository in the namespace. Name the principal by user_email or group_name (resolved through the Team and Organization Admin APIs) or by ID. The grant is identified by the resolved principal and the owner rather than a server ID. Create and update both upsert, so creating a grant for a principal that already has one adopts and replaces it. Destroying a team_group admins grant can fail with a precondition error when it is the namespace's last admin grant.",
		Attributes:  attributes,
	}
}

func (r *originOwnerGrantResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *originOwnerGrantResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var plan originOwnerGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var prior *originGrantPrincipal
	if !req.State.Raw.IsNull() {
		var state originOwnerGrantModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		statePrincipal := state.principal()
		prior = &statePrincipal
	}
	principal, key, replace, err := plan.principal().plan(ctx, r.client, prior)
	if err != nil {
		resp.Diagnostics.AddError("Failed to resolve Origin owner grant principal", err.Error())
		return
	}
	plan = plan.withPrincipal(principal)
	// The composite id follows the resolved principal and the owner. While either is unknown it stays unknown
	// rather than the prior id, which the replace that made them unknown is about to invalidate.
	plan.ID = types.StringUnknown()
	if key != nil && !plan.Owner.IsUnknown() {
		plan.ID = types.StringValue(originGrantID(plan.Owner.ValueString(), *key))
	}
	if replace {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root(key.kind))
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *originOwnerGrantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan originOwnerGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	state, err := r.upsert(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Origin owner grant", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originOwnerGrantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state originOwnerGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	key, err := state.principal().storedKey()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Origin owner grant", err.Error())
		return
	}
	found, err := r.client.findOriginGrant(ctx, state.collection(), key)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Origin owner grant", err.Error())
		return
	}
	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state = state.withPrincipal(state.principal().withKey(key))
	state.ID = types.StringValue(originGrantID(state.Owner.ValueString(), key))
	state.Permission = types.StringValue(ownerPermissionFromWire(found.Permission))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originOwnerGrantResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan originOwnerGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	state, err := r.upsert(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update Origin owner grant", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originOwnerGrantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state originOwnerGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}
	key, err := state.principal().storedKey()
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete Origin owner grant", err.Error())
		return
	}
	err = r.client.deleteOriginGrant(ctx, state.collection(), key)
	if err != nil && !isOriginNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete Origin owner grant", err.Error())
	}
}

func (r *originOwnerGrantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	owner, kind, value, err := parseOriginGrantImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	if strings.Contains(owner, "/") {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("invalid import ID %q, expected owner:principal_kind:principal without a repository", req.ID))
		return
	}
	principal, key, err := importPrincipal(ctx, r.client, kind, value)
	if err != nil {
		resp.Diagnostics.AddError("Failed to import Origin owner grant", err.Error())
		return
	}
	state := originOwnerGrantModel{
		ID:         types.StringValue(originGrantID(owner, key)),
		Owner:      types.StringValue(owner),
		Permission: types.StringNull(),
	}.withPrincipal(principal)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originOwnerGrantResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config originOwnerGrantModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateOriginOwnerGrant(config, true); err != nil {
		resp.Diagnostics.AddError("Invalid Origin owner grant", err.Error())
	}
}

func (r *originOwnerGrantResource) upsert(ctx context.Context, plan originOwnerGrantModel) (originOwnerGrantModel, error) {
	if err := validateOriginOwnerGrant(plan, false); err != nil {
		return plan, err
	}
	if plan.Owner.IsUnknown() || plan.Permission.IsUnknown() {
		return plan, fmt.Errorf("grant configuration is incomplete")
	}
	principal := plan.principal()
	key, err := principal.key(ctx, r.client, false)
	if err != nil {
		return plan, err
	}
	grant := key.wire()
	grant.Permission = ownerPermissionToWire(plan.Permission.ValueString())
	got, err := r.client.upsertOriginGrant(ctx, plan.collection(), grant)
	if err != nil {
		return plan, err
	}
	plan = plan.withPrincipal(principal.withKey(key))
	plan.ID = types.StringValue(originGrantID(plan.Owner.ValueString(), key))
	plan.Permission = types.StringValue(ownerPermissionFromWire(got.Permission))
	return plan, nil
}

func validateOriginOwnerGrant(model originOwnerGrantModel, config bool) error {
	if err := requireIDSafeSlug(model.Owner, "owner"); err != nil {
		return err
	}
	if err := validateOriginGrantPermission(model.Permission, originOwnerPermissions...); err != nil {
		return err
	}
	return model.principal().validate(config)
}
