package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*originRepoGrantResource)(nil)
	_ resource.ResourceWithImportState    = (*originRepoGrantResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*originRepoGrantResource)(nil)
	_ resource.ResourceWithValidateConfig = (*originRepoGrantResource)(nil)
)

var originRepoPermissions = []string{originPermissionRead, originPermissionWrite, originPermissionAdmin}

type originRepoGrantResource struct {
	client *apiClient
}

type originRepoGrantModel struct {
	ID         types.String `tfsdk:"id"`
	Owner      types.String `tfsdk:"owner"`
	Repo       types.String `tfsdk:"repo"`
	Permission types.String `tfsdk:"permission"`
	UserEmail  types.String `tfsdk:"user_email"`
	GroupName  types.String `tfsdk:"group_name"`
	User       types.Object `tfsdk:"user"`
	Group      types.Object `tfsdk:"group"`
	TeamGroup  types.Object `tfsdk:"team_group"`
}

func (m originRepoGrantModel) principal() originGrantPrincipal {
	return originGrantPrincipal{UserEmail: m.UserEmail, GroupName: m.GroupName, User: m.User, Group: m.Group, TeamGroup: m.TeamGroup}
}

func (m originRepoGrantModel) withPrincipal(p originGrantPrincipal) originRepoGrantModel {
	m.UserEmail, m.GroupName, m.User, m.Group, m.TeamGroup = p.UserEmail, p.GroupName, p.User, p.Group, p.TeamGroup
	return m
}

func (m originRepoGrantModel) resource() string {
	return m.Owner.ValueString() + "/" + m.Repo.ValueString()
}

func (m originRepoGrantModel) collection() string {
	return originRepoGrantsPath(m.Owner.ValueString(), m.Repo.ValueString())
}

func NewOriginRepoGrantResource() resource.Resource {
	return &originRepoGrantResource{}
}

func (r *originRepoGrantResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_repo_grant"
}

func (r *originRepoGrantResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:    true,
			Description: "Composite ID owner/repo:kind:principal built from the stored principal ID, for example acme/rocket:user:user_01... or acme/rocket:team_group:members.",
		},
		"owner": schema.StringAttribute{
			Required:    true,
			Description: "Owner slug of the repository. Changing this replaces the grant.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.RequiresReplace(),
			},
		},
		"repo": schema.StringAttribute{
			Required:    true,
			Description: "Repository name within the owner. Changing this replaces the grant.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.RequiresReplace(),
			},
		},
		"permission": schema.StringAttribute{
			Required:    true,
			Description: "Permission preset: read, write, or admin. Refresh reports custom for a grant whose policy is not a public preset; custom cannot be written, so applying replaces that policy with the configured preset.",
		},
	}
	for name, attribute := range originGrantPrincipalSchema() {
		attributes[name] = attribute
	}
	resp.Schema = schema.Schema{
		Description: "Manages one direct grant on an Origin repository: a permission preset for a user, a Cursor org group, or a team floor. Name the principal by user_email or group_name (resolved through the Team and Organization Admin APIs) or by ID. The grant is identified by the resolved principal and the repository rather than a server ID. Create and update both upsert, so creating a grant for a principal that already has one adopts and replaces it. Access inherited from namespace grants is not managed here.",
		Attributes:  attributes,
	}
}

func (r *originRepoGrantResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *originRepoGrantResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var plan originRepoGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var prior *originGrantPrincipal
	if !req.State.Raw.IsNull() {
		var state originRepoGrantModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		statePrincipal := state.principal()
		prior = &statePrincipal
	}
	principal, key, replace, err := plan.principal().plan(ctx, r.client, prior)
	if err != nil {
		resp.Diagnostics.AddError("Failed to resolve Origin repository grant principal", err.Error())
		return
	}
	plan = plan.withPrincipal(principal)
	// The composite id follows the resolved principal and the repository. While either is unknown it stays unknown
	// rather than the prior id, which the replace that made them unknown is about to invalidate.
	plan.ID = types.StringUnknown()
	if key != nil && !plan.Owner.IsUnknown() && !plan.Repo.IsUnknown() {
		plan.ID = types.StringValue(originGrantID(plan.resource(), *key))
	}
	if replace {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root(key.kind))
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *originRepoGrantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan originRepoGrantModel
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
		resp.Diagnostics.AddError("Failed to create Origin repository grant", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originRepoGrantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state originRepoGrantModel
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
		resp.Diagnostics.AddError("Failed to read Origin repository grant", err.Error())
		return
	}
	found, err := r.client.findOriginGrant(ctx, state.collection(), key)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Origin repository grant", err.Error())
		return
	}
	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state = state.withPrincipal(state.principal().withKey(key))
	state.ID = types.StringValue(originGrantID(state.resource(), key))
	state.Permission = types.StringValue(repoPermissionFromWire(found.Permission))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originRepoGrantResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan originRepoGrantModel
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
		resp.Diagnostics.AddError("Failed to update Origin repository grant", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originRepoGrantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state originRepoGrantModel
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
		resp.Diagnostics.AddError("Failed to delete Origin repository grant", err.Error())
		return
	}
	err = r.client.deleteOriginGrant(ctx, state.collection(), key)
	if err != nil && !isOriginNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete Origin repository grant", err.Error())
	}
}

func (r *originRepoGrantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	repoResource, kind, value, err := parseOriginGrantImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	owner, repo, err := splitOriginRepoResource(repoResource)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	principal, key, err := importPrincipal(ctx, r.client, kind, value)
	if err != nil {
		resp.Diagnostics.AddError("Failed to import Origin repository grant", err.Error())
		return
	}
	state := originRepoGrantModel{
		ID:         types.StringValue(originGrantID(repoResource, key)),
		Owner:      types.StringValue(owner),
		Repo:       types.StringValue(repo),
		Permission: types.StringNull(),
	}.withPrincipal(principal)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originRepoGrantResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config originRepoGrantModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateOriginRepoGrant(config, true); err != nil {
		resp.Diagnostics.AddError("Invalid Origin repository grant", err.Error())
	}
}

func (r *originRepoGrantResource) upsert(ctx context.Context, plan originRepoGrantModel) (originRepoGrantModel, error) {
	if err := validateOriginRepoGrant(plan, false); err != nil {
		return plan, err
	}
	if plan.Owner.IsUnknown() || plan.Repo.IsUnknown() || plan.Permission.IsUnknown() {
		return plan, fmt.Errorf("grant configuration is incomplete")
	}
	principal := plan.principal()
	key, err := principal.key(ctx, r.client, false)
	if err != nil {
		return plan, err
	}
	grant := key.wire()
	grant.Permission = repoPermissionToWire(plan.Permission.ValueString())
	got, err := r.client.upsertOriginGrant(ctx, plan.collection(), grant)
	if err != nil {
		return plan, err
	}
	plan = plan.withPrincipal(principal.withKey(key))
	plan.ID = types.StringValue(originGrantID(plan.resource(), key))
	plan.Permission = types.StringValue(repoPermissionFromWire(got.Permission))
	return plan, nil
}

func validateOriginRepoGrant(model originRepoGrantModel, config bool) error {
	if err := requireIDSafeSlug(model.Owner, "owner"); err != nil {
		return err
	}
	if err := requireIDSafeSlug(model.Repo, "repo"); err != nil {
		return err
	}
	if err := validateOriginGrantPermission(model.Permission, originRepoPermissions...); err != nil {
		return err
	}
	return model.principal().validate(config)
}
