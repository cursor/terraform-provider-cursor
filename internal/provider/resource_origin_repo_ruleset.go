package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*originRepoRulesetResource)(nil)
	_ resource.ResourceWithImportState    = (*originRepoRulesetResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*originRepoRulesetResource)(nil)
	_ resource.ResourceWithValidateConfig = (*originRepoRulesetResource)(nil)
)

type originRepoRulesetResource struct {
	client *apiClient
}

type originRepoRulesetModel struct {
	ID                 types.String                   `tfsdk:"id"`
	Owner              types.String                   `tfsdk:"owner"`
	Repo               types.String                   `tfsdk:"repo"`
	Name               types.String                   `tfsdk:"name"`
	Description        types.String                   `tfsdk:"description"`
	Enforcement        types.String                   `tfsdk:"enforcement"`
	Kind               types.String                   `tfsdk:"kind"`
	IncludedRefNames   types.List                     `tfsdk:"included_ref_names"`
	ExcludedRefNames   types.List                     `tfsdk:"excluded_ref_names"`
	Rules              []originRepoRulesetRuleModel   `tfsdk:"rule"`
	BypassActors       []originRepoRulesetBypassModel `tfsdk:"bypass_actor"`
	DeletionProtection types.Bool                     `tfsdk:"deletion_protection"`
	AllowUnenforced    types.Bool                     `tfsdk:"allow_unenforced"`
	AllowBroadBypass   types.Bool                     `tfsdk:"allow_broad_bypass"`
}

type originRepoRulesetRuleModel struct {
	ID         types.String    `tfsdk:"id"`
	RuleType   types.String    `tfsdk:"rule_type"`
	Parameters jsonObjectValue `tfsdk:"parameters"`
}

type originRepoRulesetBypassModel struct {
	ID         types.String                      `tfsdk:"id"`
	BypassMode types.String                      `tfsdk:"bypass_mode"`
	User       *originRepoRulesetUserModel       `tfsdk:"user"`
	Team       *originRepoRulesetTeamModel       `tfsdk:"team"`
	App        *originRepoRulesetAppModel        `tfsdk:"app"`
	OriginRole *originRepoRulesetOriginRoleModel `tfsdk:"origin_role"`
}

type originRepoRulesetUserModel struct {
	ID types.String `tfsdk:"id"`
}

type originRepoRulesetTeamModel struct {
	OrganizationPublicID types.String `tfsdk:"organization_public_id"`
	GroupPublicID        types.String `tfsdk:"group_public_id"`
}

type originRepoRulesetAppModel struct {
	ID types.String `tfsdk:"id"`
}

type originRepoRulesetOriginRoleModel struct {
	Role types.String `tfsdk:"role"`
}

func NewOriginRepoRulesetResource() resource.Resource {
	return &originRepoRulesetResource{}
}

func (r *originRepoRulesetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_repo_ruleset"
}

func (r *originRepoRulesetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an Origin repository ruleset. Update replaces rules and bypass actors. deletion_protection defaults to true, so Terraform will not delete or replace the ruleset until that is set to false and applied. A ruleset that would match no refs, contain no rules, or use enforcement disabled is rejected unless allow_unenforced is true.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Stable Origin ruleset ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"owner": schema.StringAttribute{
				Required:    true,
				Description: "Owner slug of the repository. Changing this replaces the ruleset, which deletes the existing one. Blocked while deletion_protection is true. Use \"_\" with the repository ID in repo to address the repository by its stable ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"repo": schema.StringAttribute{
				Required:    true,
				Description: "Repository name within the owner. Changing this replaces the ruleset, which deletes the existing one. Blocked while deletion_protection is true.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Ruleset name.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "Ruleset description. An empty string clears the description.",
			},
			"enforcement": schema.StringAttribute{
				Required:    true,
				Description: "Enforcement mode: active, evaluate, or disabled. disabled does not enforce the ruleset and requires allow_unenforced. evaluate records violations without blocking.",
			},
			"kind": schema.StringAttribute{
				Required:    true,
				Description: "Ruleset kind: merge_branch, push_branch, push_tag, or push_repository. Changing kind while deletion_protection is true is rejected, because it retargets the operation this ruleset protects.",
			},
			"included_ref_names": schema.ListAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Ref name patterns this ruleset includes. Supports globs and the tokens ~ALL and ~DEFAULT_BRANCH. Must contain at least one pattern, unless allow_unenforced is true and this is an explicit empty list. At most 64 entries. An empty list matches no refs.",
			},
			"excluded_ref_names": schema.ListAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Default:     listdefault.StaticValue(emptyStringList()),
				Description: "Ref name patterns this ruleset excludes. Same pattern language and 64-entry cap as included_ref_names.",
			},
			"rule": schema.ListNestedAttribute{
				Required:    true,
				Description: "Protection rules in this ruleset. Must contain at least one rule, unless allow_unenforced is true and this is an explicit empty list. At most 20 entries. Update replaces the full list; a rule omitted from config is removed.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:    true,
							Description: "Origin-assigned rule ID.",
						},
						"rule_type": schema.StringAttribute{
							Required:    true,
							Description: "Rule type, for example pull_request, require_status_checks, require_branch_up_to_date, deletion, or non_fast_forward.",
						},
						"parameters": schema.StringAttribute{
							CustomType:  jsonObjectType{},
							Optional:    true,
							Description: "JSON object of type-specific parameters. Use jsonencode(). Omit it to leave parameters unset.",
						},
					},
				},
			},
			"bypass_actor": schema.ListNestedAttribute{
				Optional:    true,
				Computed:    true,
				Default:     listdefault.StaticValue(emptyRulesetBypassList()),
				Description: "Principals that may bypass this ruleset. At most 15 entries. Set exactly one of user, team, app, or origin_role. Update replaces the full list.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:    true,
							Description: "Origin-assigned bypass actor ID.",
						},
						"bypass_mode": schema.StringAttribute{
							Required:    true,
							Description: "When the bypass applies: always, or pull_request_only.",
						},
						"user": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Bypass a user principal.",
							Attributes: map[string]schema.Attribute{
								"id": schema.StringAttribute{
									Required:    true,
									Description: "User actor ID (act_...).",
								},
							},
						},
						"team": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Bypass a team principal, identified by immutable public IDs rather than slugs.",
							Attributes: map[string]schema.Attribute{
								"organization_public_id": schema.StringAttribute{
									Required:    true,
									Description: "Organization public ID.",
								},
								"group_public_id": schema.StringAttribute{
									Required:    true,
									Description: "Group public ID.",
								},
							},
						},
						"app": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Bypass an Origin app principal.",
							Attributes: map[string]schema.Attribute{
								"id": schema.StringAttribute{
									Required:    true,
									Description: "App ID (app_...).",
								},
							},
						},
						"origin_role": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Bypass holders of a policy-backed Origin role.",
							Attributes: map[string]schema.Attribute{
								"role": schema.StringAttribute{
									Required:    true,
									Description: "Role: namespace_admin, repository_admin, or repository_write. repository_write applies to every principal with write access and requires allow_broad_bypass.",
								},
							},
						},
					},
				},
			},
			"deletion_protection": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "When true, Terraform will not delete this ruleset. That includes terraform destroy and replacements caused by changing owner or repo. Set to false and apply before destroying or moving the ruleset. Null is treated as protected.",
			},
			"allow_unenforced": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Must be true to store a ruleset that does not enforce anything: enforcement disabled, no rules, no included ref names, or excluded_ref_names that remove every included ref (including ~ALL).",
			},
			"allow_broad_bypass": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Must be true to bypass this ruleset for origin_role repository_write, which applies to every principal with write access. Specific user, team, app, and admin-role bypasses do not require this.",
			},
		},
	}
}

func (r *originRepoRulesetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *originRepoRulesetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan originRepoRulesetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}

	body, err := rulesetWriteFromModel(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Origin ruleset", err.Error())
		return
	}
	created, err := r.client.createOriginRuleset(ctx, plan.Owner.ValueString(), plan.Repo.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Origin ruleset", err.Error())
		return
	}
	state, err := originRulesetToModel(ctx, plan, created, true)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Origin ruleset", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originRepoRulesetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state originRepoRulesetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}

	ruleset, err := r.client.getOriginRuleset(ctx, state.Owner.ValueString(), state.Repo.ValueString(), state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Origin ruleset", err.Error())
		return
	}
	next, err := originRulesetToModel(ctx, state, ruleset, false)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Origin ruleset", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *originRepoRulesetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan originRepoRulesetModel
	var prior originRepoRulesetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := rulesetReplacementGuard(prior, plan); err != nil {
		resp.Diagnostics.AddError("Origin ruleset is protected from replacement", err.Error())
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}

	body, err := rulesetWriteFromModel(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Origin ruleset", err.Error())
		return
	}
	updated, err := r.client.updateOriginRuleset(ctx, plan.Owner.ValueString(), plan.Repo.ValueString(), plan.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update Origin ruleset", err.Error())
		return
	}
	state, err := originRulesetToModel(ctx, plan, updated, true)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update Origin ruleset", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *originRepoRulesetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state originRepoRulesetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}

	if err := refuseRulesetDelete(state); err != nil {
		resp.Diagnostics.AddError("Origin ruleset is protected from deletion", err.Error())
		return
	}

	err := r.client.deleteOriginRuleset(ctx, state.Owner.ValueString(), state.Repo.ValueString(), state.ID.ValueString())
	if err != nil && !isOriginNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete Origin ruleset", err.Error())
	}
}

func (r *originRepoRulesetResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan originRepoRulesetModel
	var state originRepoRulesetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := rulesetReplacementGuard(state, plan); err != nil {
		resp.Diagnostics.AddError("Origin ruleset is protected from replacement", err.Error())
	}
}

func (r *originRepoRulesetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	owner, repo, rulesetID, err := parseOriginRulesetImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner"), owner)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repo"), repo)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rulesetID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("allow_unenforced"), false)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("allow_broad_bypass"), false)...)
}

func (r *originRepoRulesetResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config originRepoRulesetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateOriginRulesetModel(ctx, config); err != nil {
		resp.Diagnostics.AddError("Invalid Origin ruleset", err.Error())
	}
}

func parseOriginRulesetImportID(id string) (string, string, string, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || strings.TrimSpace(id) != id {
		return "", "", "", fmt.Errorf("invalid import ID %q, expected owner/repo/ruleset_id", id)
	}
	return parts[0], parts[1], parts[2], nil
}

func isOriginNotFound(err error) bool {
	var apiErr *originAPIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

func validateOriginRulesetModel(ctx context.Context, model originRepoRulesetModel) error {
	if err := requireNonEmptyUnpadded(model.Name, "name"); err != nil {
		return err
	}
	if err := requireKnownEnum(model.Enforcement, "enforcement", originRulesetEnforcementActive, originRulesetEnforcementEvaluate, originRulesetEnforcementDisabled); err != nil {
		return err
	}
	if err := requireKnownEnum(model.Kind, "kind", originRulesetKindMergeBranch, originRulesetKindPushBranch, originRulesetKindPushTag, originRulesetKindPushRepository); err != nil {
		return err
	}
	included, includedKnown, err := stringList(ctx, model.IncludedRefNames)
	if err != nil {
		return fmt.Errorf("included_ref_names: %w", err)
	}
	excluded, excludedKnown, err := stringList(ctx, model.ExcludedRefNames)
	if err != nil {
		return fmt.Errorf("excluded_ref_names: %w", err)
	}
	if includedKnown && len(included) > maxOriginIncludedRefs {
		return fmt.Errorf("included_ref_names has %d entries; the Origin API allows at most %d", len(included), maxOriginIncludedRefs)
	}
	if excludedKnown && len(excluded) > maxOriginIncludedRefs {
		return fmt.Errorf("excluded_ref_names has %d entries; the Origin API allows at most %d", len(excluded), maxOriginIncludedRefs)
	}
	if len(model.Rules) > maxOriginRules {
		return fmt.Errorf("rule has %d entries; the Origin API allows at most %d", len(model.Rules), maxOriginRules)
	}
	if len(model.BypassActors) > maxOriginBypassActors {
		return fmt.Errorf("bypass_actor has %d entries; the Origin API allows at most %d", len(model.BypassActors), maxOriginBypassActors)
	}
	for i, rule := range model.Rules {
		if err := validateOriginRule(i, rule); err != nil {
			return err
		}
	}
	for i, actor := range model.BypassActors {
		if err := validateOriginBypassActor(i, actor); err != nil {
			return err
		}
	}
	if err := validateRefs(included, includedKnown, "included_ref_names"); err != nil {
		return err
	}
	if err := validateRefs(excluded, excludedKnown, "excluded_ref_names"); err != nil {
		return err
	}
	if !boolEnabled(model.AllowUnenforced) {
		if reason, unenforced := rulesetUnenforced(model, includedKnown, included, excludedKnown, excluded); unenforced {
			return fmt.Errorf("%s; set allow_unenforced = true to store a ruleset that does not enforce anything", reason)
		}
	}
	if !boolEnabled(model.AllowBroadBypass) {
		if err := rejectBroadBypass(model.BypassActors); err != nil {
			return err
		}
	}
	return nil
}

func validateRefs(values []string, known bool, name string) error {
	if !known {
		return nil
	}
	for i, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s[%d] must not be empty or padded", name, i)
		}
	}
	return nil
}

func rulesetUnenforced(model originRepoRulesetModel, includedKnown bool, included []string, excludedKnown bool, excluded []string) (string, bool) {
	if !model.Enforcement.IsNull() && !model.Enforcement.IsUnknown() && model.Enforcement.ValueString() == originRulesetEnforcementDisabled {
		return "enforcement is disabled", true
	}
	if rulesKnown(model.Rules) && len(model.Rules) == 0 {
		return "rule is empty", true
	}
	if excludedKnown && containsRefToken(excluded, "~ALL") {
		return "excluded_ref_names contains ~ALL, which excludes every ref", true
	}
	if includedKnown && !refsStillCovered(included, excluded) {
		return "included_ref_names matches no refs after excluded_ref_names", true
	}
	return "", false
}

func rulesKnown(rules []originRepoRulesetRuleModel) bool {
	for _, rule := range rules {
		if rule.RuleType.IsUnknown() {
			return false
		}
	}
	return true
}

func refsStillCovered(included, excluded []string) bool {
	if len(included) == 0 {
		return false
	}
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, item := range excluded {
		excludedSet[item] = struct{}{}
	}
	if _, ok := excludedSet["~ALL"]; ok {
		return false
	}
	for _, item := range included {
		if item == "~ALL" {
			return true
		}
		if _, ok := excludedSet[item]; !ok {
			return true
		}
	}
	return false
}

func containsRefToken(values []string, token string) bool {
	for _, value := range values {
		if value == token {
			return true
		}
	}
	return false
}

func rejectBroadBypass(actors []originRepoRulesetBypassModel) error {
	for i, actor := range actors {
		if actor.OriginRole == nil || actor.OriginRole.Role.IsNull() || actor.OriginRole.Role.IsUnknown() {
			continue
		}
		if actor.OriginRole.Role.ValueString() == originRoleRepositoryWrite {
			return fmt.Errorf("bypass_actor[%d] grants repository_write bypass, which applies to every principal with write access; set allow_broad_bypass = true to store it", i)
		}
	}
	return nil
}

func boolEnabled(value types.Bool) bool {
	return !value.IsNull() && !value.IsUnknown() && value.ValueBool()
}

func deletionProtectionEnabled(value types.Bool) bool {
	return value.IsNull() || value.IsUnknown() || value.ValueBool()
}

func refuseRulesetDelete(state originRepoRulesetModel) error {
	if deletionProtectionEnabled(state.DeletionProtection) {
		return fmt.Errorf("deletion_protection is enabled; set deletion_protection = false and apply before destroying or replacing this ruleset")
	}
	return nil
}

func rulesetReplacementGuard(state, plan originRepoRulesetModel) error {
	if !deletionProtectionEnabled(state.DeletionProtection) {
		return nil
	}
	if knownStringChanged(state.Owner, plan.Owner) || knownStringChanged(state.Repo, plan.Repo) {
		return fmt.Errorf("changing owner or repo deletes the existing ruleset; set deletion_protection = false and apply before moving it")
	}
	if knownStringChanged(state.Kind, plan.Kind) {
		return fmt.Errorf("changing kind retargets what this ruleset protects; set deletion_protection = false and apply before changing kind")
	}
	return nil
}

func knownStringChanged(state, plan types.String) bool {
	if state.IsNull() || state.IsUnknown() || plan.IsNull() || plan.IsUnknown() {
		return false
	}
	return state.ValueString() != plan.ValueString()
}

func validateOriginRule(index int, rule originRepoRulesetRuleModel) error {
	name := fmt.Sprintf("rule[%d]", index)
	if !rule.RuleType.IsUnknown() {
		if err := requireNonEmptyUnpadded(rule.RuleType, name+".rule_type"); err != nil {
			return err
		}
	}
	if rule.Parameters.IsUnknown() {
		return nil
	}
	if _, err := jsonObjectRaw(rule.Parameters); err != nil {
		return fmt.Errorf("%s.parameters: %w", name, err)
	}
	return nil
}

func validateOriginBypassActor(index int, actor originRepoRulesetBypassModel) error {
	name := fmt.Sprintf("bypass_actor[%d]", index)
	if err := requireKnownEnum(actor.BypassMode, name+".bypass_mode", originBypassModeAlways, originBypassModePullRequestOnly); err != nil {
		return err
	}

	set := 0
	if actor.User != nil {
		set++
		if !actor.User.ID.IsUnknown() {
			if err := requireNonEmptyUnpadded(actor.User.ID, name+".user.id"); err != nil {
				return err
			}
			if err := rejectBroadPrincipalID(actor.User.ID.ValueString(), name+".user.id"); err != nil {
				return err
			}
		}
	}
	if actor.Team != nil {
		set++
		if !actor.Team.OrganizationPublicID.IsUnknown() {
			if err := requireNonEmptyUnpadded(actor.Team.OrganizationPublicID, name+".team.organization_public_id"); err != nil {
				return err
			}
			if err := rejectBroadPrincipalID(actor.Team.OrganizationPublicID.ValueString(), name+".team.organization_public_id"); err != nil {
				return err
			}
		}
		if !actor.Team.GroupPublicID.IsUnknown() {
			if err := requireNonEmptyUnpadded(actor.Team.GroupPublicID, name+".team.group_public_id"); err != nil {
				return err
			}
			if err := rejectBroadPrincipalID(actor.Team.GroupPublicID.ValueString(), name+".team.group_public_id"); err != nil {
				return err
			}
		}
	}
	if actor.App != nil {
		set++
		if !actor.App.ID.IsUnknown() {
			if err := requireNonEmptyUnpadded(actor.App.ID, name+".app.id"); err != nil {
				return err
			}
			if err := rejectBroadPrincipalID(actor.App.ID.ValueString(), name+".app.id"); err != nil {
				return err
			}
		}
	}
	if actor.OriginRole != nil {
		set++
		if err := requireKnownEnum(actor.OriginRole.Role, name+".origin_role.role", originRoleNamespaceAdmin, originRoleRepositoryAdmin, originRoleRepositoryWrite); err != nil {
			return err
		}
	}
	if set != 1 && !(set == 0 && actor.BypassMode.IsUnknown()) {
		return fmt.Errorf("%s must set exactly one of user, team, app, or origin_role", name)
	}
	return nil
}

func requireKnownEnum(value types.String, name string, allowed ...string) error {
	if value.IsUnknown() {
		return nil
	}
	if value.IsNull() {
		return fmt.Errorf("%s is required", name)
	}
	got := value.ValueString()
	if strings.TrimSpace(got) != got {
		return fmt.Errorf("%s must not have leading or trailing spaces", name)
	}
	for _, candidate := range allowed {
		if got == candidate {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of %s", name, strings.Join(allowed, ", "))
}

func rejectBroadPrincipalID(value, name string) error {
	switch strings.ToLower(value) {
	case "~all", "*", "all", "everyone", "~everyone", "~default_branch":
		return fmt.Errorf("%s %q is not a principal ID and is rejected so a token cannot bypass every matching actor", name, value)
	default:
		return nil
	}
}

func requireNonEmptyUnpadded(value types.String, name string) error {
	if value.IsUnknown() {
		return nil
	}
	if value.IsNull() || value.ValueString() == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.TrimSpace(value.ValueString()) != value.ValueString() {
		return fmt.Errorf("%s must not have leading or trailing spaces", name)
	}
	return nil
}

func rulesetWriteFromModel(ctx context.Context, model originRepoRulesetModel) (originRulesetWrite, error) {
	if err := validateOriginRulesetModel(ctx, model); err != nil {
		return originRulesetWrite{}, err
	}
	if model.Name.IsUnknown() || model.Enforcement.IsUnknown() || model.Kind.IsUnknown() || model.IncludedRefNames.IsUnknown() || model.ExcludedRefNames.IsUnknown() {
		return originRulesetWrite{}, fmt.Errorf("ruleset configuration is incomplete")
	}
	included, _, err := stringList(ctx, model.IncludedRefNames)
	if err != nil {
		return originRulesetWrite{}, err
	}
	excluded, _, err := stringList(ctx, model.ExcludedRefNames)
	if err != nil {
		return originRulesetWrite{}, err
	}
	rules := make([]originRulesetRuleInput, 0, len(model.Rules))
	for i, rule := range model.Rules {
		if rule.RuleType.IsUnknown() {
			return originRulesetWrite{}, fmt.Errorf("rule[%d].rule_type is incomplete", i)
		}
		parameters, err := jsonObjectRaw(rule.Parameters)
		if err != nil {
			return originRulesetWrite{}, fmt.Errorf("rule[%d].parameters: %w", i, err)
		}
		rules = append(rules, originRulesetRuleInput{
			RuleType:   rule.RuleType.ValueString(),
			Parameters: parameters,
		})
	}
	actors := make([]originRulesetBypassActorInput, 0, len(model.BypassActors))
	for _, actor := range model.BypassActors {
		actors = append(actors, bypassActorInputFromModel(actor))
	}
	description := ""
	if !model.Description.IsNull() && !model.Description.IsUnknown() {
		description = model.Description.ValueString()
	}
	return originRulesetWrite{
		Name:             model.Name.ValueString(),
		Description:      description,
		Enforcement:      model.Enforcement.ValueString(),
		Kind:             model.Kind.ValueString(),
		IncludedRefNames: included,
		ExcludedRefNames: excluded,
		Rules:            rules,
		BypassActors:     actors,
	}, nil
}

func bypassActorInputFromModel(actor originRepoRulesetBypassModel) originRulesetBypassActorInput {
	input := originRulesetBypassActorInput{
		BypassMode: actor.BypassMode.ValueString(),
	}
	if actor.User != nil {
		input.User = &originRulesetUserBypass{ID: actor.User.ID.ValueString()}
	}
	if actor.Team != nil {
		input.Team = &originRulesetTeamBypass{
			OrganizationPublicID: actor.Team.OrganizationPublicID.ValueString(),
			GroupPublicID:        actor.Team.GroupPublicID.ValueString(),
		}
	}
	if actor.App != nil {
		input.App = &originRulesetAppBypass{ID: actor.App.ID.ValueString()}
	}
	if actor.OriginRole != nil {
		input.OriginRole = &originRulesetOriginRoleBypass{Role: actor.OriginRole.Role.ValueString()}
	}
	return input
}

func originRulesetToModel(ctx context.Context, prior originRepoRulesetModel, ruleset *originRuleset, apply bool) (originRepoRulesetModel, error) {
	if ruleset == nil {
		return originRepoRulesetModel{}, fmt.Errorf("Origin API returned an empty ruleset")
	}
	if prior.Owner.IsNull() || prior.Owner.IsUnknown() || prior.Repo.IsNull() || prior.Repo.IsUnknown() {
		return originRepoRulesetModel{}, fmt.Errorf("ruleset owner and repo are required")
	}

	rules, err := alignRules(prior.Rules, ruleset.Rules, apply)
	if err != nil {
		return originRepoRulesetModel{}, err
	}
	actors, err := alignBypassActors(prior.BypassActors, ruleset.BypassActors, apply)
	if err != nil {
		return originRepoRulesetModel{}, err
	}
	included, err := stringListValue(ctx, ruleset.IncludedRefNames)
	if err != nil {
		return originRepoRulesetModel{}, err
	}
	excluded, err := stringListValue(ctx, ruleset.ExcludedRefNames)
	if err != nil {
		return originRepoRulesetModel{}, err
	}

	return originRepoRulesetModel{
		ID:                 types.StringValue(ruleset.ID),
		Owner:              prior.Owner,
		Repo:               prior.Repo,
		Name:               types.StringValue(ruleset.Name),
		Description:        types.StringValue(ruleset.Description),
		Enforcement:        types.StringValue(ruleset.Enforcement),
		Kind:               types.StringValue(ruleset.Kind),
		IncludedRefNames:   included,
		ExcludedRefNames:   excluded,
		Rules:              rules,
		BypassActors:       actors,
		DeletionProtection: boolOrDefault(prior.DeletionProtection, true),
		AllowUnenforced:    boolOrDefault(prior.AllowUnenforced, false),
		AllowBroadBypass:   boolOrDefault(prior.AllowBroadBypass, false),
	}, nil
}

func boolOrDefault(value types.Bool, fallback bool) types.Bool {
	if value.IsNull() || value.IsUnknown() {
		return types.BoolValue(fallback)
	}
	return value
}

func alignRules(prior []originRepoRulesetRuleModel, api []originRulesetRule, apply bool) ([]originRepoRulesetRuleModel, error) {
	used := make([]bool, len(api))
	out := make([]originRepoRulesetRuleModel, 0, len(prior))
	for i, want := range prior {
		match := findMatchingRule(want, api, used)
		if match < 0 {
			if apply {
				return nil, fmt.Errorf("Origin API omitted requested rule[%d] %s", i, want.RuleType.ValueString())
			}
			continue
		}
		used[match] = true
		mapped, err := ruleStateFromAPI(want, api[match], true)
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	for i, rule := range api {
		if used[i] {
			continue
		}
		if apply {
			return nil, fmt.Errorf("Origin API returned unrequested rule %q", rule.ID)
		}
		mapped, err := ruleFromAPI(rule)
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	return out, nil
}

func findMatchingRule(want originRepoRulesetRuleModel, api []originRulesetRule, used []bool) int {
	if want.RuleType.IsNull() || want.RuleType.IsUnknown() {
		return -1
	}
	wantType := want.RuleType.ValueString()
	fallback := -1
	for i, rule := range api {
		if used[i] || rule.RuleType != wantType {
			continue
		}
		if want.Parameters.IsNull() || want.Parameters.IsUnknown() {
			if apiParametersEmpty(rule.Parameters) {
				return i
			}
			if fallback < 0 {
				fallback = i
			}
			continue
		}
		if parametersSemanticallyEqual(want.Parameters, rule.Parameters) {
			return i
		}
		if fallback < 0 {
			fallback = i
		}
	}
	return fallback
}

func ruleStateFromAPI(want originRepoRulesetRuleModel, rule originRulesetRule, havePrior bool) (originRepoRulesetRuleModel, error) {
	if strings.TrimSpace(rule.ID) == "" {
		return originRepoRulesetRuleModel{}, fmt.Errorf("Origin API returned a rule without an id")
	}
	parameters, err := jsonObjectFromRaw(rule.Parameters, want.Parameters, havePrior)
	if err != nil {
		return originRepoRulesetRuleModel{}, fmt.Errorf("rule %q parameters: %w", rule.ID, err)
	}
	ruleType := types.StringValue(rule.RuleType)
	if havePrior && !want.RuleType.IsNull() && !want.RuleType.IsUnknown() {
		ruleType = want.RuleType
	}
	return originRepoRulesetRuleModel{
		ID:         types.StringValue(rule.ID),
		RuleType:   ruleType,
		Parameters: parameters,
	}, nil
}

func ruleFromAPI(rule originRulesetRule) (originRepoRulesetRuleModel, error) {
	return ruleStateFromAPI(originRepoRulesetRuleModel{}, rule, false)
}

func alignBypassActors(prior []originRepoRulesetBypassModel, api []originRulesetBypassActor, apply bool) ([]originRepoRulesetBypassModel, error) {
	used := make([]bool, len(api))
	out := make([]originRepoRulesetBypassModel, 0, len(prior))
	for i, want := range prior {
		key, ok := bypassKeyFromModel(want)
		if !ok {
			return nil, fmt.Errorf("bypass_actor[%d] must set exactly one of user, team, app, or origin_role", i)
		}
		match := -1
		for j, actor := range api {
			if used[j] {
				continue
			}
			got, err := bypassKeyFromAPI(actor)
			if err != nil {
				return nil, err
			}
			if got == key {
				match = j
				break
			}
		}
		if match < 0 {
			if apply {
				return nil, fmt.Errorf("Origin API omitted requested bypass actor %s", key)
			}
			continue
		}
		used[match] = true
		mapped, err := bypassStateFromAPI(want, api[match], true, apply)
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	for i, actor := range api {
		if used[i] {
			continue
		}
		if apply {
			return nil, fmt.Errorf("Origin API returned unrequested bypass actor %q", actor.ID)
		}
		mapped, err := bypassFromAPI(actor)
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	return out, nil
}

type bypassKey struct {
	kind string
	a    string
	b    string
}

func (k bypassKey) String() string {
	if k.b == "" {
		return k.kind + " " + k.a
	}
	return k.kind + " " + k.a + "/" + k.b
}

func bypassKeyFromModel(actor originRepoRulesetBypassModel) (bypassKey, bool) {
	set := 0
	var key bypassKey
	if actor.User != nil && !actor.User.ID.IsNull() && !actor.User.ID.IsUnknown() {
		set++
		key = bypassKey{kind: "user", a: actor.User.ID.ValueString()}
	}
	if actor.Team != nil && !actor.Team.OrganizationPublicID.IsNull() && !actor.Team.OrganizationPublicID.IsUnknown() && !actor.Team.GroupPublicID.IsNull() && !actor.Team.GroupPublicID.IsUnknown() {
		set++
		key = bypassKey{kind: "team", a: actor.Team.OrganizationPublicID.ValueString(), b: actor.Team.GroupPublicID.ValueString()}
	}
	if actor.App != nil && !actor.App.ID.IsNull() && !actor.App.ID.IsUnknown() {
		set++
		key = bypassKey{kind: "app", a: actor.App.ID.ValueString()}
	}
	if actor.OriginRole != nil && !actor.OriginRole.Role.IsNull() && !actor.OriginRole.Role.IsUnknown() {
		set++
		key = bypassKey{kind: "origin_role", a: actor.OriginRole.Role.ValueString()}
	}
	if set != 1 {
		return bypassKey{}, false
	}
	return key, true
}

func bypassKeyFromAPI(actor originRulesetBypassActor) (bypassKey, error) {
	set := 0
	var key bypassKey
	if actor.User != nil {
		set++
		key = bypassKey{kind: "user", a: actor.User.ID}
	}
	if actor.Team != nil {
		set++
		key = bypassKey{kind: "team", a: actor.Team.OrganizationPublicID, b: actor.Team.GroupPublicID}
	}
	if actor.App != nil {
		set++
		key = bypassKey{kind: "app", a: actor.App.ID}
	}
	if actor.OriginRole != nil {
		set++
		key = bypassKey{kind: "origin_role", a: actor.OriginRole.Role}
	}
	if set != 1 {
		return bypassKey{}, fmt.Errorf("Origin API returned bypass actor %q without exactly one principal", actor.ID)
	}
	return key, nil
}

func bypassStateFromAPI(want originRepoRulesetBypassModel, actor originRulesetBypassActor, havePrior, apply bool) (originRepoRulesetBypassModel, error) {
	if strings.TrimSpace(actor.ID) == "" {
		return originRepoRulesetBypassModel{}, fmt.Errorf("Origin API returned a bypass actor without an id")
	}
	mode := types.StringValue(actor.BypassMode)
	if apply && havePrior && !want.BypassMode.IsNull() && !want.BypassMode.IsUnknown() {
		mode = want.BypassMode
	}
	mapped := originRepoRulesetBypassModel{
		ID:         types.StringValue(actor.ID),
		BypassMode: mode,
	}
	if havePrior {
		mapped.User = want.User
		mapped.Team = want.Team
		mapped.App = want.App
		mapped.OriginRole = want.OriginRole
		return mapped, nil
	}
	return bypassFromAPI(actor)
}

func bypassFromAPI(actor originRulesetBypassActor) (originRepoRulesetBypassModel, error) {
	if _, err := bypassKeyFromAPI(actor); err != nil {
		return originRepoRulesetBypassModel{}, err
	}
	mapped := originRepoRulesetBypassModel{
		ID:         types.StringValue(actor.ID),
		BypassMode: types.StringValue(actor.BypassMode),
	}
	if actor.User != nil {
		mapped.User = &originRepoRulesetUserModel{ID: types.StringValue(actor.User.ID)}
	}
	if actor.Team != nil {
		mapped.Team = &originRepoRulesetTeamModel{
			OrganizationPublicID: types.StringValue(actor.Team.OrganizationPublicID),
			GroupPublicID:        types.StringValue(actor.Team.GroupPublicID),
		}
	}
	if actor.App != nil {
		mapped.App = &originRepoRulesetAppModel{ID: types.StringValue(actor.App.ID)}
	}
	if actor.OriginRole != nil {
		mapped.OriginRole = &originRepoRulesetOriginRoleModel{Role: types.StringValue(actor.OriginRole.Role)}
	}
	if strings.TrimSpace(actor.ID) == "" {
		return originRepoRulesetBypassModel{}, fmt.Errorf("Origin API returned a bypass actor without an id")
	}
	return mapped, nil
}

func apiParametersEmpty(raw json.RawMessage) bool {
	canonical, err := canonicalJSONObject(string(raw))
	return err == nil && canonical == "{}"
}

func parametersSemanticallyEqual(prior jsonObjectValue, raw json.RawMessage) bool {
	canonical, err := canonicalJSONObject(string(raw))
	if err != nil {
		return false
	}
	priorCanonical, err := canonicalJSONObject(prior.ValueString())
	return err == nil && priorCanonical == canonical
}

func stringList(ctx context.Context, value types.List) ([]string, bool, error) {
	if value.IsNull() || value.IsUnknown() {
		return []string{}, !value.IsUnknown(), nil
	}
	var items []string
	diags := value.ElementsAs(ctx, &items, false)
	if diags.HasError() {
		return nil, false, fmt.Errorf("%s", diags)
	}
	if items == nil {
		return []string{}, true, nil
	}
	return items, true, nil
}

func stringListValue(ctx context.Context, values []string) (types.List, error) {
	if values == nil {
		values = []string{}
	}
	list, diags := types.ListValueFrom(ctx, types.StringType, values)
	if diags.HasError() {
		return types.ListNull(types.StringType), fmt.Errorf("%s", diags)
	}
	return list, nil
}

func emptyStringList() types.List {
	return types.ListValueMust(types.StringType, []attr.Value{})
}

func emptyRulesetBypassList() types.List {
	return types.ListValueMust(types.ObjectType{AttrTypes: originRulesetBypassAttrTypes()}, []attr.Value{})
}

func originRulesetBypassAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"id":          types.StringType,
		"bypass_mode": types.StringType,
		"user": types.ObjectType{AttrTypes: map[string]attr.Type{
			"id": types.StringType,
		}},
		"team": types.ObjectType{AttrTypes: map[string]attr.Type{
			"organization_public_id": types.StringType,
			"group_public_id":        types.StringType,
		}},
		"app": types.ObjectType{AttrTypes: map[string]attr.Type{
			"id": types.StringType,
		}},
		"origin_role": types.ObjectType{AttrTypes: map[string]attr.Type{
			"role": types.StringType,
		}},
	}
}
