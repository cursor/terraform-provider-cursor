package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	connect "connectrpc.com/connect"
	v1 "github.com/cursor/terraform-provider-cursor/internal/proto/v1"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/encoding/protowire"
)

// connectErrorMessage extracts a human-readable error message from a Connect
// error. The backend sends a fixed "Error" string as the wire message to avoid
// leaking secrets and stashes the real user-facing detail inside an ErrorDetails
// proto. The standalone provider intentionally avoids generating the full
// utils.proto file, so we decode just ErrorDetails.details.title/detail from the
// wire bytes.
func connectErrorMessage(err error) string {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return err.Error()
	}

	for _, d := range connectErr.Details() {
		if d.Type() != "aiserver.v1.ErrorDetails" {
			continue
		}
		title, detail := parseErrorDetailsTitleDetail(d.Bytes())
		parts := make([]string, 0, 2)
		if title != "" {
			parts = append(parts, title)
		}
		if detail != "" && detail != title {
			parts = append(parts, detail)
		}
		if len(parts) > 0 {
			return strings.Join(parts, ": ")
		}
	}

	if connectErr.Message() != "" && connectErr.Message() != "Error" {
		return fmt.Sprintf("%s: %s", connectErr.Code(), connectErr.Message())
	}
	return err.Error()
}

func parseErrorDetailsTitleDetail(b []byte) (string, string) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", ""
		}
		b = b[n:]
		if num != 2 || typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return "", ""
			}
			b = b[n:]
			continue
		}
		details, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return "", ""
		}
		return parseCustomErrorDetailsTitleDetail(details)
	}
	return "", ""
}

func parseCustomErrorDetailsTitleDetail(b []byte) (string, string) {
	var title string
	var detail string
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return title, detail
		}
		b = b[n:]
		if typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return title, detail
			}
			b = b[n:]
			continue
		}
		value, n := protowire.ConsumeString(b)
		if n < 0 {
			return title, detail
		}
		switch num {
		case 1:
			title = value
		case 2:
			detail = value
		}
		b = b[n:]
	}
	return title, detail
}

// ---------------------------------------------------------------------------
// Terraform state models
// ---------------------------------------------------------------------------

type platformWorkflowModel struct {
	ID                   types.String         `tfsdk:"id"`
	Name                 types.String         `tfsdk:"name"`
	Description          types.String         `tfsdk:"description"`
	Scope                types.String         `tfsdk:"scope"`
	TeamID               types.Int64          `tfsdk:"team_id"`
	Enabled              types.Bool           `tfsdk:"enabled"`
	Prompt               types.String         `tfsdk:"prompt"`
	EffortLevel          types.String         `tfsdk:"effort_level"`
	Model                types.String         `tfsdk:"model"`
	ModelSelection       *modelSelectionModel `tfsdk:"model_selection"`
	GitRepo              types.String         `tfsdk:"git_repo"`
	GitBranch            types.String         `tfsdk:"git_branch"`
	SkipInstall          types.Bool           `tfsdk:"skip_install"`
	EnvironmentPublicID  types.String         `tfsdk:"environment_public_id"`
	PrivateWorker        *privateWorkerModel  `tfsdk:"private_worker"`
	MemoryEnabled        types.Bool           `tfsdk:"memory_enabled"`
	DisabledDefaultTools types.List           `tfsdk:"disabled_default_tools"`
	Triggers             []triggerModel       `tfsdk:"trigger"`
	Actions              []actionModel        `tfsdk:"action"`
	CreatedAt            types.Int64          `tfsdk:"created_at"`
	UpdatedAt            types.Int64          `tfsdk:"updated_at"`
}

type privateWorkerModel struct {
	Labels types.Map `tfsdk:"labels"`
}

// modelSelectionModel mirrors AutomationModelSelection: the structured twin of
// the legacy `model` slug, able to carry parameters the slug cannot (e.g. the
// Auto tier via optimize_for).
type modelSelectionModel struct {
	ModelID    types.String                   `tfsdk:"model_id"`
	Parameters []modelSelectionParameterModel `tfsdk:"parameters"`
	MaxMode    types.Bool                     `tfsdk:"max_mode"`
}

type modelSelectionParameterModel struct {
	ID    types.String `tfsdk:"id"`
	Value types.String `tfsdk:"value"`
}

type triggerModel struct {
	GitPullRequest               *gitPullRequestModel                      `tfsdk:"git_pull_request"`
	GitPush                      *gitPushModel                             `tfsdk:"git_push"`
	GitCICompleted               *gitCICompletedModel                      `tfsdk:"git_ci_completed"`
	Cron                         *cronModel                                `tfsdk:"cron"`
	Slack                        *slackTriggerModel                        `tfsdk:"slack"`
	SlackChannelCreated          *slackChannelCreatedTriggerModel          `tfsdk:"slack_channel_created"`
	SlackReactionAdded           *slackReactionAddedTriggerModel           `tfsdk:"slack_reaction_added"`
	SlackMention                 *slackMentionTriggerModel                 `tfsdk:"slack_mention"`
	SlackAnyReactionAdded        *slackAnyReactionAddedTriggerModel        `tfsdk:"slack_any_reaction_added"`
	Linear                       *linearTriggerModel                       `tfsdk:"linear"`
	Webhook                      *webhookTriggerModel                      `tfsdk:"webhook"`
	PagerDuty                    *pagerDutyTriggerModel                    `tfsdk:"pagerduty"`
	Sentry                       *sentryTriggerModel                       `tfsdk:"sentry"`
	MicrosoftTeams               *microsoftTeamsTriggerModel               `tfsdk:"microsoft_teams"`
	MicrosoftTeamsChannelCreated *microsoftTeamsChannelCreatedTriggerModel `tfsdk:"microsoft_teams_channel_created"`
	UserAllowlist                types.List                                `tfsdk:"user_allowlist"`
}

// triggerTypeNames lists the trigger block names in schema order, used for
// the "exactly one trigger type" error message.
const triggerTypeNames = "git_pull_request, git_push, git_ci_completed, cron, slack, slack_channel_created, slack_reaction_added, slack_mention, slack_any_reaction_added, linear, webhook, pagerduty, sentry, microsoft_teams, or microsoft_teams_channel_created"

const actionTypeNames = "pr_comment, git_pr, request_reviewers, mcp, slack, read_slack, microsoft_teams, read_microsoft_teams, manage_check_run, approve_pr, or resolve_review_threads"

type gitPullRequestModel struct {
	Orgs                   types.List   `tfsdk:"orgs"`
	Repos                  types.List   `tfsdk:"repos"`
	IgnoreDraftPrs         types.Bool   `tfsdk:"ignore_draft_prs"`
	PrAction               types.String `tfsdk:"pr_action"`
	CommentContains        types.String `tfsdk:"comment_contains"`
	CommentContainsIsRegex types.Bool   `tfsdk:"comment_contains_is_regex"`
}

type gitPushModel struct {
	Repo   types.String `tfsdk:"repo"`
	Branch types.String `tfsdk:"branch"`
}

type gitCICompletedModel struct {
	Repos              types.List   `tfsdk:"repos"`
	Condition          types.String `tfsdk:"condition"`
	IgnoreBaseFailures types.Bool   `tfsdk:"ignore_base_failures"`
	Branch             types.String `tfsdk:"branch"`
}

type cronModel struct {
	Schedule types.String `tfsdk:"schedule"`
}

type slackTriggerModel struct {
	Channel                        types.String `tfsdk:"channel"`
	MessageContains                types.String `tfsdk:"message_contains"`
	MessageContainsIsRegex         types.Bool   `tfsdk:"message_contains_is_regex"`
	BlockUnauthenticatedSlackUsers types.Bool   `tfsdk:"block_unauthenticated_slack_users"`
	CompletionReactionMode         types.String `tfsdk:"completion_reaction_mode"`
	CompletionReactionCustomEmoji  types.String `tfsdk:"completion_reaction_custom_emoji"`
}

type slackChannelCreatedTriggerModel struct {
	ChannelNameContains types.String `tfsdk:"channel_name_contains"`
}

type slackReactionAddedTriggerModel struct {
	Channel                        types.String `tfsdk:"channel"`
	EmojiName                      types.String `tfsdk:"emoji_name"`
	BlockUnauthenticatedSlackUsers types.Bool   `tfsdk:"block_unauthenticated_slack_users"`
	OnlyOwnerReactions             types.Bool   `tfsdk:"only_owner_reactions"`
}

type slackMentionTriggerModel struct {
	Channel                        types.String `tfsdk:"channel"`
	BlockUnauthenticatedSlackUsers types.Bool   `tfsdk:"block_unauthenticated_slack_users"`
}

type slackAnyReactionAddedTriggerModel struct {
	Channel                        types.String `tfsdk:"channel"`
	BlockUnauthenticatedSlackUsers types.Bool   `tfsdk:"block_unauthenticated_slack_users"`
	OnlyOwnerReactions             types.Bool   `tfsdk:"only_owner_reactions"`
}

type pagerDutyTriggerModel struct {
	IncidentTriggered    *emptyEventModel `tfsdk:"incident_triggered"`
	IncidentAcknowledged *emptyEventModel `tfsdk:"incident_acknowledged"`
	IncidentResolved     *emptyEventModel `tfsdk:"incident_resolved"`
	IncidentEscalated    *emptyEventModel `tfsdk:"incident_escalated"`
	IncidentAny          *emptyEventModel `tfsdk:"incident_any"`
	ServiceIDs           types.List       `tfsdk:"service_ids"`
}

type sentryTriggerModel struct {
	IssueCreated    *emptyEventModel `tfsdk:"issue_created"`
	IssueResolved   *emptyEventModel `tfsdk:"issue_resolved"`
	IssueAssigned   *emptyEventModel `tfsdk:"issue_assigned"`
	IssueArchived   *emptyEventModel `tfsdk:"issue_archived"`
	IssueUnresolved *emptyEventModel `tfsdk:"issue_unresolved"`
	IssueAny        *emptyEventModel `tfsdk:"issue_any"`
	ProjectIDs      types.List       `tfsdk:"project_ids"`
}

// emptyEventModel is a marker block: presence selects the event type.
type emptyEventModel struct{}

type linearTriggerModel struct {
	IssueCreated  *linearIssueCreatedModel  `tfsdk:"issue_created"`
	StatusChanged *linearStatusChangedModel `tfsdk:"status_changed"`
	EndOfCycle    *linearEndOfCycleModel    `tfsdk:"end_of_cycle"`
	ProjectIDs    types.List                `tfsdk:"project_ids"`
	TeamIDs       types.List                `tfsdk:"team_ids"`
}

type linearIssueCreatedModel struct {
	// Empty: triggers on issue creation.
}

type linearStatusChangedModel struct {
	StatusIDs types.List `tfsdk:"status_ids"`
}

type linearEndOfCycleModel struct {
	CycleIDs types.List `tfsdk:"cycle_ids"`
}

type webhookTriggerModel struct {
	// Empty: webhook trigger has no configuration fields.
}

type microsoftTeamsTriggerModel struct {
	TenantID                       types.String `tfsdk:"tenant_id"`
	TeamID                         types.String `tfsdk:"team_id"`
	TeamIDs                        types.List   `tfsdk:"team_ids"`
	ChannelIDs                     types.List   `tfsdk:"channel_ids"`
	MessageContains                types.String `tfsdk:"message_contains"`
	MessageContainsIsRegex         types.Bool   `tfsdk:"message_contains_is_regex"`
	BlockUnauthenticatedTeamsUsers types.Bool   `tfsdk:"block_unauthenticated_teams_users"`
}

type microsoftTeamsChannelCreatedTriggerModel struct {
	TenantID            types.String `tfsdk:"tenant_id"`
	TeamIDs             types.List   `tfsdk:"team_ids"`
	ChannelNameContains types.String `tfsdk:"channel_name_contains"`
}

type actionModel struct {
	PrComment            *prCommentActionModel            `tfsdk:"pr_comment"`
	GitPr                *gitPrActionModel                `tfsdk:"git_pr"`
	RequestReviewers     *requestReviewersActionModel     `tfsdk:"request_reviewers"`
	Mcp                  *mcpActionModel                  `tfsdk:"mcp"`
	Slack                *slackActionModel                `tfsdk:"slack"`
	ReadSlack            *readSlackActionModel            `tfsdk:"read_slack"`
	MicrosoftTeams       *microsoftTeamsActionModel       `tfsdk:"microsoft_teams"`
	ReadMicrosoftTeams   *readMicrosoftTeamsActionModel   `tfsdk:"read_microsoft_teams"`
	ManageCheckRun       *manageCheckRunActionModel       `tfsdk:"manage_check_run"`
	ApprovePr            *approvePrActionModel            `tfsdk:"approve_pr"`
	ResolveReviewThreads *resolveReviewThreadsActionModel `tfsdk:"resolve_review_threads"`
}

type manageCheckRunActionModel struct {
	// Empty: check runs are always system-managed.
}

type approvePrActionModel struct {
	// Empty: deprecated in favour of pr_comment.allow_approve.
}

type resolveReviewThreadsActionModel struct {
	// Empty: adds the ResolveReviewThreads tool.
}

type prCommentActionModel struct {
	AllowInlineComments types.Bool `tfsdk:"allow_inline_comments"`
	AllowApprove        types.Bool `tfsdk:"allow_approve"`
}

type gitPrActionModel struct {
	// Empty for now, but allows future expansion
}

type requestReviewersActionModel struct {
	// Empty for now, but allows future expansion
}

type mcpActionModel struct {
	Server   types.String `tfsdk:"server"`
	ServerID types.Int64  `tfsdk:"server_id"`
}

type slackActionModel struct {
	Channel         types.String `tfsdk:"channel"`
	Generalized     types.Bool   `tfsdk:"generalized"`
	RespondInThread types.Bool   `tfsdk:"respond_in_thread"`
	PostAsThread    types.Bool   `tfsdk:"post_as_thread"`
}

type readSlackActionModel struct {
	// Empty: gives agent read-only access to public Slack channels
}

type microsoftTeamsActionModel struct {
	TenantID        types.String `tfsdk:"tenant_id"`
	TeamID          types.String `tfsdk:"team_id"`
	ChannelID       types.String `tfsdk:"channel_id"`
	ChannelIDs      types.List   `tfsdk:"channel_ids"`
	Generalized     types.Bool   `tfsdk:"generalized"`
	RespondInThread types.Bool   `tfsdk:"respond_in_thread"`
	PostAsThread    types.Bool   `tfsdk:"post_as_thread"`
}

type readMicrosoftTeamsActionModel struct {
	// Empty: gives agent read-only access to Microsoft Teams channels.
}

// ---------------------------------------------------------------------------
// Resource
// ---------------------------------------------------------------------------

type platformWorkflowResource struct {
	client *apiClient
}

var (
	_ resource.ResourceWithImportState    = (*platformWorkflowResource)(nil)
	_ resource.ResourceWithValidateConfig = (*platformWorkflowResource)(nil)
)

func NewPlatformWorkflowResource() resource.Resource {
	return &platformWorkflowResource{}
}

func (r *platformWorkflowResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_platform_workflow"
}

func (r *platformWorkflowResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Cursor Automation: a prompt plus triggers (pull requests, pushes, CI completions, cron, Slack, Linear, webhooks, PagerDuty, Sentry, Microsoft Teams) and actions (PR comments, PRs, reviewers, check runs, MCP servers, Slack, Microsoft Teams).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Automation ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Display name for the automation.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "Optional free-text description shown in the Cursor dashboard.",
			},
			"team_id": schema.Int64Attribute{
				Optional:    true,
				Description: "Numeric Cursor team ID to create and read the automation under. When unset, the team associated with the token is used. Requires a user token that is a member (or org admin) of the team; service-account tokens must leave this unset. Changing it between two set values forces a new automation because an automation cannot move between teams.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplaceIf(
						func(_ context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
							resp.RequiresReplace = !req.StateValue.IsNull() && !req.PlanValue.IsNull() && !req.PlanValue.Equal(req.StateValue)
						},
						"Replaces the automation when team_id changes from one team to another.",
						"Replaces the automation when `team_id` changes from one team to another.",
					),
				},
			},
			"scope": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: `Automation ownership scope: "user", "team", "team_visible", "team_editable_user", or "team_editable". "user" is private (owner and admins only), "team" is shared (team admins can edit, runs as team service account), "team_visible" is viewable by team (team can view, only owner can edit, runs as owner), "team_editable_user" is editable by the team but still runs as the creator user, and "team_editable" is editable by the team and runs as the team service account. Defaults to "user" when unset.`,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Whether the automation is enabled.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"prompt": schema.StringAttribute{
				Required:    true,
				Description: "The prompt text that defines what the agent should do.",
			},
			"effort_level": schema.StringAttribute{
				Optional:    true,
				Description: `Effort level for the prompt: "standard" or "hard". Defaults to standard if unset.`,
			},
			"model": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Legacy model slug (e.g. claude-4.6-opus-high-thinking, gpt-4o). If unset, the server assigns a default model. Cannot be combined with model_selection: when model_selection is set the server derives this value from it.",
				PlanModifiers: []planmodifier.String{
					modelUseStateUnlessSelectionChanged{},
				},
			},
			"model_selection": schema.SingleNestedAttribute{
				Optional:    true,
				Description: `Structured model choice: a catalog model ID plus parameters the legacy model slug cannot carry. Use it to pick an Auto tier, e.g. model_id = "auto-smart" with parameters = [{ id = "optimize_for", value = "cost" }] (Auto Cost) or value = "balanced" (Auto Balance) or "intelligence". Takes priority over model at run time; leave model unset when using it.`,
				Attributes: map[string]schema.Attribute{
					"model_id": schema.StringAttribute{
						Required:    true,
						Description: `Catalog model ID (e.g. "auto-smart"). Use the canonical ID: the server rejects unknown IDs and rewrites aliases, which would show up as a diff.`,
					},
					"parameters": schema.ListNestedAttribute{
						Optional:    true,
						Computed:    true,
						Description: `Parameter values selecting a model variant, e.g. { id = "optimize_for", value = "cost" }. Parameter IDs must be unique. When omitted the server picks the model's default variant and reports its parameters here.`,
						PlanModifiers: []planmodifier.List{
							modelSelectionParametersUseStateUnlessModelChanged{},
						},
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"id": schema.StringAttribute{
									Required:    true,
									Description: "Parameter ID (e.g. optimize_for).",
								},
								"value": schema.StringAttribute{
									Required:    true,
									Description: `Parameter value. Enum parameters take one of their enum values (e.g. "cost", "balanced", "intelligence" for optimize_for); booleans take "true"/"false".`,
								},
							},
						},
					},
					"max_mode": schema.BoolAttribute{
						Optional:    true,
						Computed:    true,
						Description: "Run the model in max mode. Defaults to true and is reported back as true; the server currently rejects false.",
						PlanModifiers: []planmodifier.Bool{
							boolplanmodifier.UseStateForUnknown(),
						},
					},
				},
			},
			"git_repo": schema.StringAttribute{
				Optional:    true,
				Description: "Git repository for non-git triggers (cron, slack, linear). E.g. github.com/org/repo.",
			},
			"git_branch": schema.StringAttribute{
				Optional:    true,
				Description: "Git branch for non-git triggers. Defaults to main.",
			},
			"skip_install": schema.BoolAttribute{
				Optional:    true,
				Description: "Skip user install commands and cloud testing.",
			},
			"environment_public_id": schema.StringAttribute{
				Optional:    true,
				Description: "Public ID of the Cloud Agent environment this automation should run in.",
			},
			"private_worker": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Route this automation to private workers. An empty object targets any private worker; labels narrow the eligible workers.",
				Attributes: map[string]schema.Attribute{
					"labels": schema.MapAttribute{
						Optional:    true,
						Computed:    true,
						ElementType: types.StringType,
						Default: mapdefault.StaticValue(types.MapValueMust(
							types.StringType,
							map[string]attr.Value{},
						)),
						Description: "Private-worker selector labels. Label keys and values are matched exactly.",
					},
				},
			},
			"memory_enabled": schema.BoolAttribute{
				Optional:    true,
				Description: "Enable the AutomationMemory tool, giving the agent persistent memory across runs.",
			},
			"disabled_default_tools": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: `Default automation tools to withhold from the agent. Supported values: "open_git_pr". Leave unset to keep the platform defaults.`,
			},
			"trigger": schema.ListNestedAttribute{
				Required:    true,
				Description: "One or more triggers that start the automation.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"git_pull_request": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger on GitHub pull request events.",
							Attributes: map[string]schema.Attribute{
								"orgs": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									Description: "GitHub orgs to watch (e.g. example-org).",
								},
								"repos": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									Description: "GitHub repos to watch (e.g. org/repo).",
								},
								"ignore_draft_prs": schema.BoolAttribute{
									Optional:    true,
									Computed:    true,
									Description: "Do not trigger on draft PRs.",
								},
								"pr_action": schema.StringAttribute{
									Optional:    true,
									Description: `PR action to trigger on: "opened", "pushed", "merged", "commented". Triggers on opened+pushed if unset.`,
								},
								"comment_contains": schema.StringAttribute{
									Optional:    true,
									Description: "Only trigger if the comment body contains this text (case-insensitive). Only used when pr_action is \"commented\".",
								},
								"comment_contains_is_regex": schema.BoolAttribute{
									Optional:    true,
									Computed:    true,
									Description: "If true, comment_contains is treated as a regex pattern (case-insensitive).",
								},
							},
						},
						"git_push": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger on git push events.",
							Attributes: map[string]schema.Attribute{
								"repo": schema.StringAttribute{
									Required:    true,
									Description: "Repository to watch.",
								},
								"branch": schema.StringAttribute{
									Optional:    true,
									Description: "Branch to watch.",
								},
							},
						},
						"git_ci_completed": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger when all CI checks complete on a PR (PR mode) or a specific branch (branch mode).",
							Attributes: map[string]schema.Attribute{
								"repos": schema.ListAttribute{
									Required:    true,
									ElementType: types.StringType,
									Description: "GitHub repos to watch (e.g. org/repo). At least one is required.",
								},
								"condition": schema.StringAttribute{
									Optional:    true,
									Description: `Which CI outcome fires the trigger: "failure", "success", or "any". Server default applies if unset.`,
								},
								"ignore_base_failures": schema.BoolAttribute{
									Optional:    true,
									Computed:    true,
									Description: "If true, ignore CI failures that also exist on the base branch. Only applies in PR mode.",
								},
								"branch": schema.StringAttribute{
									Optional:    true,
									Description: `If set, trigger on CI completion for this branch (e.g. "main") instead of on PRs. user_allowlist and ignore_base_failures are ignored in branch mode.`,
								},
							},
						},
						"cron": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger on a cron schedule.",
							Attributes: map[string]schema.Attribute{
								"schedule": schema.StringAttribute{
									Required:    true,
									Description: "Cron expression (e.g. 0 9 * * *).",
								},
							},
						},
						"slack": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger on Slack messages.",
							Attributes: map[string]schema.Attribute{
								"channel": schema.StringAttribute{
									Required:    true,
									Description: "Slack channel ID.",
								},
								"message_contains": schema.StringAttribute{
									Optional:    true,
									Description: "Only trigger if message contains this text (case-insensitive).",
								},
								"message_contains_is_regex": schema.BoolAttribute{
									Optional:    true,
									Description: "If true, message_contains is treated as a regex pattern (case-insensitive).",
								},
								"block_unauthenticated_slack_users": schema.BoolAttribute{
									Optional:    true,
									Description: "If true, only Slack users who linked Cursor can trigger. Omit/false = anyone (default).",
								},
								"completion_reaction_mode": schema.StringAttribute{
									Optional: true,
									// Computed: the API reports the default ("on") even when
									// unset; without Computed, applies fail with an
									// inconsistent-result error and unset configs drift.
									Computed:    true,
									Description: `Controls the emoji reaction added to the triggering Slack message when the automation completes successfully: "on" (default Cursor reaction), "off" (no reaction), or "custom" (use completion_reaction_custom_emoji). Leave unset to use the Cursor default.`,
								},
								"completion_reaction_custom_emoji": schema.StringAttribute{
									Optional:    true,
									Description: `Custom Slack reaction emoji in ":emoji_name:" form. Only used when completion_reaction_mode is "custom".`,
								},
							},
						},
						"slack_channel_created": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger when a public Slack channel is created.",
							Attributes: map[string]schema.Attribute{
								"channel_name_contains": schema.StringAttribute{
									Optional:    true,
									Description: "Only trigger if the new channel name contains this text (case-insensitive).",
								},
							},
						},
						"slack_reaction_added": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger when a specific emoji reaction is added to a message in a Slack channel.",
							Attributes: map[string]schema.Attribute{
								"channel": schema.StringAttribute{
									Required:    true,
									Description: "Slack channel ID.",
								},
								"emoji_name": schema.StringAttribute{
									Required:    true,
									Description: `Slack emoji short name without colons, lowercase (e.g. "thumbsup", "white_check_mark").`,
								},
								"block_unauthenticated_slack_users": schema.BoolAttribute{
									Optional:    true,
									Description: "If true, only Slack users who linked Cursor can trigger. Omit/false = anyone (default).",
								},
								"only_owner_reactions": schema.BoolAttribute{
									Optional:    true,
									Description: "If true, only the automation owner's own linked Slack user can trigger it. Stricter than block_unauthenticated_slack_users.",
								},
							},
						},
						"slack_mention": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger when the Cursor Slack app is mentioned in a Slack channel.",
							Attributes: map[string]schema.Attribute{
								"channel": schema.StringAttribute{
									Required:    true,
									Description: "Slack channel ID.",
								},
								"block_unauthenticated_slack_users": schema.BoolAttribute{
									Optional:    true,
									Description: "If true, only Slack users who linked Cursor can trigger. Omit/false = anyone (default).",
								},
							},
						},
						"slack_any_reaction_added": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger when any emoji reaction is added to a message in a Slack channel.",
							Attributes: map[string]schema.Attribute{
								"channel": schema.StringAttribute{
									Required:    true,
									Description: "Slack channel ID.",
								},
								"block_unauthenticated_slack_users": schema.BoolAttribute{
									Optional:    true,
									Description: "If true, only Slack users who linked Cursor can trigger. Omit/false = anyone (default).",
								},
								"only_owner_reactions": schema.BoolAttribute{
									Optional:    true,
									Description: "If true, only the automation owner's own linked Slack user can trigger it. Stricter than block_unauthenticated_slack_users.",
								},
							},
						},
						"linear": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger on Linear events.",
							Attributes: map[string]schema.Attribute{
								"issue_created": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger when a Linear issue is created.",
									Attributes:  map[string]schema.Attribute{},
								},
								"status_changed": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger when a Linear issue status changes.",
									Attributes: map[string]schema.Attribute{
										"status_ids": schema.ListAttribute{
											Optional:    true,
											ElementType: types.StringType,
											Description: "Optional Linear status IDs to match.",
										},
									},
								},
								"end_of_cycle": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger at the end of a Linear cycle.",
									Attributes: map[string]schema.Attribute{
										"cycle_ids": schema.ListAttribute{
											Optional:    true,
											ElementType: types.StringType,
											Description: "Optional Linear cycle IDs to match.",
										},
									},
								},
								"project_ids": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									Description: "Optional Linear project IDs to scope issue events.",
								},
								"team_ids": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									Description: "Optional Linear team IDs to scope cycle events.",
								},
							},
						},
						"webhook": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger on generic webhook POST requests.",
							Attributes:  map[string]schema.Attribute{},
						},
						"pagerduty": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger on PagerDuty incident events. Exactly one of incident_triggered, incident_acknowledged, incident_resolved, incident_escalated, or incident_any must be set.",
							Attributes: map[string]schema.Attribute{
								"incident_triggered": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger when an incident is triggered.",
									Attributes:  map[string]schema.Attribute{},
								},
								"incident_acknowledged": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger when an incident is acknowledged.",
									Attributes:  map[string]schema.Attribute{},
								},
								"incident_resolved": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger when an incident is resolved.",
									Attributes:  map[string]schema.Attribute{},
								},
								"incident_escalated": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger when an incident is escalated.",
									Attributes:  map[string]schema.Attribute{},
								},
								"incident_any": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger on any incident event (triggered, acknowledged, resolved, escalated).",
									Attributes:  map[string]schema.Attribute{},
								},
								"service_ids": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									Description: "Optional PagerDuty service IDs to scope the trigger to. Empty fires for all services.",
								},
							},
						},
						"sentry": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger on Sentry issue webhooks. Exactly one of issue_created, issue_resolved, issue_assigned, issue_archived, issue_unresolved, or issue_any must be set. The Cursor Sentry integration must be connected for the owner before the automation can be saved.",
							Attributes: map[string]schema.Attribute{
								"issue_created": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger when a Sentry issue is created.",
									Attributes:  map[string]schema.Attribute{},
								},
								"issue_resolved": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger when a Sentry issue is resolved.",
									Attributes:  map[string]schema.Attribute{},
								},
								"issue_assigned": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger when a Sentry issue is assigned.",
									Attributes:  map[string]schema.Attribute{},
								},
								"issue_archived": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger when a Sentry issue is archived.",
									Attributes:  map[string]schema.Attribute{},
								},
								"issue_unresolved": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger when a Sentry issue is marked unresolved.",
									Attributes:  map[string]schema.Attribute{},
								},
								"issue_any": schema.SingleNestedAttribute{
									Optional:    true,
									Description: "Trigger on any Sentry issue event.",
									Attributes:  map[string]schema.Attribute{},
								},
								"project_ids": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									Description: "Optional Sentry numeric project IDs (as strings) to scope the trigger to. Empty matches all projects in the organization.",
								},
							},
						},
						"microsoft_teams": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger on Microsoft Teams channel messages.",
							Attributes: map[string]schema.Attribute{
								"tenant_id": schema.StringAttribute{
									Required:    true,
									Description: "AAD tenant GUID hosting the team.",
								},
								"team_id": schema.StringAttribute{
									Optional:    true,
									Description: "AAD group ID for a single configured team. One of team_id or team_ids is required.",
								},
								"team_ids": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									Description: "AAD group IDs for multiple teams. Takes precedence over team_id when populated.",
								},
								"channel_ids": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									Description: "Optional Microsoft Teams channel IDs (e.g. 19:abc@thread.tacv2). When empty, fires for any channel in the configured team(s).",
								},
								"message_contains": schema.StringAttribute{
									Optional:    true,
									Description: "Only trigger if message contains this text (case-insensitive).",
								},
								"message_contains_is_regex": schema.BoolAttribute{
									Optional:    true,
									Description: "If true, message_contains is treated as a regex pattern (case-insensitive).",
								},
								"block_unauthenticated_teams_users": schema.BoolAttribute{
									Optional:    true,
									Description: "If true, only Microsoft Teams users who linked Cursor can trigger. Omit/false = anyone (default).",
								},
							},
						},
						"microsoft_teams_channel_created": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Trigger when a new Microsoft Teams channel is created in a configured team.",
							Attributes: map[string]schema.Attribute{
								"tenant_id": schema.StringAttribute{
									Required:    true,
									Description: "AAD tenant GUID hosting the team.",
								},
								"team_ids": schema.ListAttribute{
									Required:    true,
									ElementType: types.StringType,
									Description: "AAD group IDs of the teams to watch. At least one is required.",
								},
								"channel_name_contains": schema.StringAttribute{
									Optional:    true,
									Description: "Only trigger if the new channel name contains this text (case-insensitive).",
								},
							},
						},
						"user_allowlist": schema.ListAttribute{
							Optional:    true,
							ElementType: types.StringType,
							Description: "Git usernames allowed to trigger this automation. Empty means all users.",
						},
					},
				},
			},
			"action": schema.ListNestedAttribute{
				Optional:    true,
				Description: "Actions the automation can perform. Each action block specifies one action type.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"pr_comment": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Post a comment on the PR.",
							Attributes: map[string]schema.Attribute{
								"allow_inline_comments": schema.BoolAttribute{
									Optional:    true,
									Computed:    true,
									Description: "If true, the agent can post a PR review with inline comments on specific diff lines; if false or unset, only a single top-level comment is posted.",
								},
								"allow_approve": schema.BoolAttribute{
									Optional:    true,
									Computed:    true,
									Description: "If true, the agent can approve or dismiss approvals on the PR using the PR comment tool.",
								},
							},
						},
						"git_pr": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Create a pull request.",
							Attributes:  map[string]schema.Attribute{},
						},
						"request_reviewers": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Request reviewers on the PR.",
							Attributes:  map[string]schema.Attribute{},
						},
						"mcp": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Enable an MCP server for this automation.",
							Attributes: map[string]schema.Attribute{
								"server": schema.StringAttribute{
									Required:    true,
									Description: "MCP server name.",
								},
								"server_id": schema.Int64Attribute{
									Optional:    true,
									Computed:    true,
									Description: "Stable MCP server ID. When set, the server is resolved by ID; server remains for display and backwards compatibility.",
								},
							},
						},
						"slack": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Post messages to a Slack channel.",
							Attributes: map[string]schema.Attribute{
								"channel": schema.StringAttribute{
									Optional:    true,
									Description: "Slack channel ID to post to.",
								},
								"generalized": schema.BoolAttribute{
									Optional:    true,
									Computed:    true,
									Description: "If true, agent can list and send to any Slack channel or DM dynamically.",
								},
								"respond_in_thread": schema.BoolAttribute{
									Optional:           true,
									Computed:           true,
									Description:        "Deprecated: the server ignores this flag and always replies in the triggering Slack thread. Kept for compatibility with existing configurations.",
									DeprecationMessage: "The server ignores respond_in_thread and always replies in the triggering Slack thread; remove it from your configuration.",
								},
								"post_as_thread": schema.BoolAttribute{
									Optional:    true,
									Computed:    true,
									Description: "If true, post a parent message with the automation name and reply in the thread.",
								},
							},
						},
						"read_slack": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Give the agent read-only access to public Slack channels (ListSlackChannels, ReadSlackMessages tools).",
							Attributes:  map[string]schema.Attribute{},
						},
						"manage_check_run": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Create and resolve a GitHub check run on the PR for each automation run. Only relevant for git_pull_request triggers; the check run lifecycle is always system-managed.",
							Attributes:  map[string]schema.Attribute{},
						},
						"approve_pr": schema.SingleNestedAttribute{
							Optional:           true,
							Description:        "Deprecated: allow the agent to approve the PR. Use pr_comment.allow_approve instead.",
							DeprecationMessage: "approve_pr is deprecated server-side; set pr_comment.allow_approve = true instead.",
							Attributes:         map[string]schema.Attribute{},
						},
						"resolve_review_threads": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Let the agent mark its own prior PR review threads as addressed and resolve them on GitHub (ResolveReviewThreads tool).",
							Attributes:  map[string]schema.Attribute{},
						},
						"microsoft_teams": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Post messages to a Microsoft Teams channel.",
							Attributes: map[string]schema.Attribute{
								"tenant_id": schema.StringAttribute{
									Optional:    true,
									Description: "AAD tenant GUID for the destination team. Required when generalized = false.",
								},
								"team_id": schema.StringAttribute{
									Optional:    true,
									Description: "AAD group ID of the destination team. Required when generalized = false and channel_ids is empty.",
								},
								"channel_id": schema.StringAttribute{
									Optional:    true,
									Description: "Microsoft Teams channel ID to post to. Mirrors the slack action's channel field.",
								},
								"channel_ids": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									Description: "Multiple Teams channel IDs (within team_id). Takes precedence over channel_id.",
								},
								"generalized": schema.BoolAttribute{
									Optional:    true,
									Computed:    true,
									Description: "If true, the agent can list and post to any team/channel dynamically.",
								},
								"respond_in_thread": schema.BoolAttribute{
									Optional:    true,
									Computed:    true,
									Description: "If true, respond in the thread of the triggering Microsoft Teams message (Teams triggers only).",
								},
								"post_as_thread": schema.BoolAttribute{
									Optional:    true,
									Computed:    true,
									Description: "If true, post a parent message with the automation name and reply in the thread.",
								},
							},
						},
						"read_microsoft_teams": schema.SingleNestedAttribute{
							Optional:    true,
							Description: "Give the agent read-only access to Microsoft Teams channels (ListMicrosoftTeamsChannels, ReadMicrosoftTeamsMessages tools).",
							Attributes:  map[string]schema.Attribute{},
						},
					},
				},
			},
			"created_at": schema.Int64Attribute{
				Computed:    true,
				Description: "Unix timestamp (seconds) when the automation was created.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.Int64Attribute{
				Computed:    true,
				Description: "Unix timestamp (seconds) when the automation was last updated.",
			},
		},
	}
}

func (r *platformWorkflowResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *platformWorkflowResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan platformWorkflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Async platform client is unavailable.")
		return
	}

	workflow, err := modelToWorkflow(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid automation configuration", err.Error())
		return
	}
	scope, err := parseAutomationScope(plan.Scope)
	if err != nil {
		resp.Diagnostics.AddError("Invalid automation configuration", err.Error())
		return
	}

	createReq := &v1.CreateAutomationRequest{
		Name:     plan.Name.ValueString(),
		Scope:    scope,
		Workflow: workflow,
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() && plan.Description.ValueString() != "" {
		description := plan.Description.ValueString()
		createReq.Description = &description
	}
	if teamID := optionalTeamID(plan.TeamID); teamID != nil {
		createReq.TeamId = teamID
	}

	createResp, err := r.client.automations.CreateAutomation(ctx, connect.NewRequest(createReq))
	if err != nil {
		resp.Diagnostics.AddError("Failed to create automation", connectErrorMessage(err))
		return
	}

	state, err := protoToModel(ctx, createResp.Msg.GetWorkflow())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read automation", err.Error())
		return
	}
	preserveConfiguredValues(ctx, &state, plan)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if shouldDisable(plan.Enabled) {
		updated, err := r.updateEnabled(ctx, state, false)
		if err != nil {
			resp.Diagnostics.AddWarning(
				"Automation created but failed to disable",
				fmt.Sprintf("The automation was created successfully but could not be disabled: %s. "+
					"Run terraform apply again to retry.", connectErrorMessage(err)),
			)
			return
		}
		preserveConfiguredValues(ctx, &updated, plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &updated)...)
	}
}

// preserveConfiguredValues rewrites server-normalised values back to the
// practitioner-supplied form when equivalent, and carries over attributes the
// API does not echo back (team_id).
func preserveConfiguredValues(ctx context.Context, state *platformWorkflowModel, reference platformWorkflowModel) {
	preserveEquivalentGitPullRequestOrgs(ctx, state, reference)
	preserveEquivalentGitCICompletionConditions(state, reference)
	preserveEquivalentEnvironmentPublicID(state, reference)
	preserveEquivalentModelSelection(state, reference)
	preserveEmptyDescription(state, reference)
	state.TeamID = reference.TeamID
}

// An explicitly empty description is never sent and reads back as null; keep
// the configured "" so the state matches the plan.
func preserveEmptyDescription(state *platformWorkflowModel, reference platformWorkflowModel) {
	if state.Description.IsNull() && !reference.Description.IsNull() && !reference.Description.IsUnknown() && reference.Description.ValueString() == "" {
		state.Description = reference.Description
	}
}

func optionalTeamID(value types.Int64) *int32 {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	teamID := int32(value.ValueInt64())
	return &teamID
}

// automationFromGetResponse unwraps GetAutomationResponse, turning the
// restricted-summary variant (the token can see that the automation exists but
// not its definition) into a clear error instead of an "empty response" one.
func automationFromGetResponse(msg *v1.GetAutomationResponse) (*v1.AutomationWithOwner, error) {
	if summary := msg.GetRestrictedSummary(); summary != nil {
		return nil, fmt.Errorf(
			"automation %s (%q, owned by %q) is only visible as a restricted summary: the configured token cannot read its definition. Use a token belonging to the owner or a team admin, or set team_id to the owning team",
			summary.GetAutomationId(), summary.GetName(), summary.GetOwnerName(),
		)
	}
	return msg.GetWorkflow(), nil
}

func (r *platformWorkflowResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state platformWorkflowModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Async platform client is unavailable.")
		return
	}

	workflowID, err := parseWorkflowID(state.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid automation ID", err.Error())
		return
	}

	workflowResp, err := r.client.automations.GetAutomation(
		ctx,
		connect.NewRequest(&v1.GetAutomationRequest{
			AutomationId: workflowID,
			TeamId:       optionalTeamID(state.TeamID),
		}),
	)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read automation", connectErrorMessage(err))
		return
	}

	withOwner, err := automationFromGetResponse(workflowResp.Msg)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read automation", err.Error())
		return
	}
	updatedState, err := protoToModel(ctx, withOwner)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read automation", err.Error())
		return
	}
	preserveConfiguredValues(ctx, &updatedState, state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &updatedState)...)
}

func (r *platformWorkflowResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan platformWorkflowModel
	var state platformWorkflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Async platform client is unavailable.")
		return
	}

	workflowID, err := parseWorkflowID(state.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid automation ID", err.Error())
		return
	}

	// Always rebuild the full workflow from the plan and send it. The API
	// handles partial updates via the optional fields on the request, but
	// for workflow definition changes it is safest to send the complete
	// definition so the server replaces the whole thing atomically.
	workflow, err := modelToWorkflow(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid automation configuration", err.Error())
		return
	}

	updateReq := &v1.UpdateAutomationRequest{
		AutomationId: workflowID,
		Workflow:     workflow,
	}

	if !plan.Name.IsUnknown() && !plan.Name.IsNull() && plan.Name.ValueString() != state.Name.ValueString() {
		name := plan.Name.ValueString()
		updateReq.Name = &name
	}
	if shouldUpdateDescription(plan.Description, state.Description) {
		// A removed description is sent as "" so the server clears it; the
		// empty value reads back as null, matching the plan.
		description := plan.Description.ValueString()
		updateReq.Description = &description
	}

	if shouldUpdateEnabled(plan.Enabled, state.Enabled) {
		enabled := plan.Enabled.ValueBool()
		updateReq.Enabled = &enabled
	}
	if shouldUpdateScope(plan.Scope, state.Scope) {
		scope, err := parseAutomationScope(plan.Scope)
		if err != nil {
			resp.Diagnostics.AddError("Invalid automation configuration", err.Error())
			return
		}
		updateReq.Scope = scope
	}

	updateResp, err := r.client.automations.UpdateAutomation(ctx, connect.NewRequest(updateReq))
	if err != nil {
		resp.Diagnostics.AddError("Failed to update automation", connectErrorMessage(err))
		return
	}

	updatedState, err := protoToModel(ctx, updateResp.Msg.GetWorkflow())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read automation", err.Error())
		return
	}
	preserveConfiguredValues(ctx, &updatedState, plan)

	resp.Diagnostics.Append(resp.State.Set(ctx, &updatedState)...)
}

func (r *platformWorkflowResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state platformWorkflowModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Async platform client is unavailable.")
		return
	}

	workflowID, err := parseWorkflowID(state.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid automation ID", err.Error())
		return
	}

	_, err = r.client.automations.DeleteAutomation(
		ctx,
		connect.NewRequest(&v1.DeleteAutomationRequest{AutomationId: workflowID}),
	)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return
		}
		resp.Diagnostics.AddError("Failed to delete automation", connectErrorMessage(err))
	}
}

func (r *platformWorkflowResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ValidateConfig rejects model together with model_selection: the server
// overwrites model from the validated selection, so a configured slug that
// disagrees with it could never be applied consistently.
func (r *platformWorkflowResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var model types.String
	var selection types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("model"), &model)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("model_selection"), &selection)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Unknown values may still resolve to null; only reject when both are known.
	if model.IsUnknown() || selection.IsUnknown() {
		return
	}
	if !model.IsNull() && !selection.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("model"),
			"Conflicting model configuration",
			"model cannot be set together with model_selection; the server derives model from model_selection. Remove model.",
		)
	}
}

// ---------------------------------------------------------------------------
// model / model_selection plan modifiers
// ---------------------------------------------------------------------------

// modelSelectionAttrs extracts the parts of a model_selection object value,
// tolerating unknown nested values.
type modelSelectionAttrs struct {
	set        bool
	modelID    types.String
	parameters types.List
	maxMode    types.Bool
}

func modelSelectionAttrsFrom(obj types.Object) modelSelectionAttrs {
	if obj.IsNull() || obj.IsUnknown() {
		return modelSelectionAttrs{}
	}
	attrs := obj.Attributes()
	out := modelSelectionAttrs{set: true}
	if v, ok := attrs["model_id"].(types.String); ok {
		out.modelID = v
	}
	if v, ok := attrs["parameters"].(types.List); ok {
		out.parameters = v
	}
	if v, ok := attrs["max_mode"].(types.Bool); ok {
		out.maxMode = v
	}
	return out
}

// modelSelectionChangedForPlan reports whether the planned model_selection
// differs from state in a way that makes the server re-derive model (and the
// default variant parameters). Unknown planned parameters/max_mode are treated
// as unchanged: they are only unknown because they were not configured, and the
// server keeps them when model_id is unchanged.
func modelSelectionChangedForPlan(ctx context.Context, plan tfsdk.Plan, state tfsdk.State) bool {
	var planObj, stateObj types.Object
	if diags := plan.GetAttribute(ctx, path.Root("model_selection"), &planObj); diags.HasError() {
		return true
	}
	if diags := state.GetAttribute(ctx, path.Root("model_selection"), &stateObj); diags.HasError() {
		return true
	}
	if planObj.IsUnknown() {
		return true
	}
	p := modelSelectionAttrsFrom(planObj)
	s := modelSelectionAttrsFrom(stateObj)
	if p.set != s.set {
		return true
	}
	if !p.set {
		return false
	}
	if p.modelID.IsUnknown() || !p.modelID.Equal(s.modelID) {
		return true
	}
	if !p.maxMode.IsUnknown() && !p.maxMode.Equal(s.maxMode) {
		return true
	}
	if !p.parameters.IsUnknown() && !p.parameters.Equal(s.parameters) {
		return true
	}
	return false
}

// modelUseStateUnlessSelectionChanged behaves like UseStateForUnknown for the
// computed model slug, except when model_selection changed: the server then
// rewrites model from the new selection, so the value must stay unknown.
type modelUseStateUnlessSelectionChanged struct{}

func (modelUseStateUnlessSelectionChanged) Description(_ context.Context) string {
	return "Keeps the prior model unless model_selection changed."
}

func (m modelUseStateUnlessSelectionChanged) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (modelUseStateUnlessSelectionChanged) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	if !req.PlanValue.IsUnknown() || req.StateValue.IsUnknown() {
		return
	}
	if modelSelectionChangedForPlan(ctx, req.Plan, req.State) {
		return
	}
	resp.PlanValue = req.StateValue
}

// modelSelectionParametersUseStateUnlessModelChanged keeps the server-reported
// default-variant parameters across plans while model_id (and max_mode) stay
// the same; a new model_id gets fresh defaults, so the value stays unknown.
type modelSelectionParametersUseStateUnlessModelChanged struct{}

func (modelSelectionParametersUseStateUnlessModelChanged) Description(_ context.Context) string {
	return "Keeps the prior parameters unless model_selection.model_id changed."
}

func (m modelSelectionParametersUseStateUnlessModelChanged) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (modelSelectionParametersUseStateUnlessModelChanged) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	if !req.PlanValue.IsUnknown() || req.StateValue.IsUnknown() {
		return
	}
	if modelSelectionChangedForPlan(ctx, req.Plan, req.State) {
		return
	}
	resp.PlanValue = req.StateValue
}

func (r *platformWorkflowResource) updateEnabled(ctx context.Context, state platformWorkflowModel, enabled bool) (platformWorkflowModel, error) {
	workflowID, err := parseWorkflowID(state.ID)
	if err != nil {
		return platformWorkflowModel{}, err
	}
	updateResp, err := r.client.automations.UpdateAutomation(
		ctx,
		connect.NewRequest(&v1.UpdateAutomationRequest{
			AutomationId: workflowID,
			Enabled:      &enabled,
		}),
	)
	if err != nil {
		return platformWorkflowModel{}, err
	}
	return protoToModel(ctx, updateResp.Msg.GetWorkflow())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func parseWorkflowID(value types.String) (string, error) {
	if value.IsNull() || value.IsUnknown() {
		return "", fmt.Errorf("automation ID is not set")
	}
	id := value.ValueString()
	if id == "" {
		return "", fmt.Errorf("automation ID is empty")
	}
	return id, nil
}

func shouldDisable(value types.Bool) bool {
	return !value.IsNull() && !value.IsUnknown() && !value.ValueBool()
}

func shouldUpdateEnabled(plan types.Bool, state types.Bool) bool {
	if plan.IsNull() || plan.IsUnknown() {
		return false
	}
	if state.IsNull() || state.IsUnknown() {
		return true
	}
	return plan.ValueBool() != state.ValueBool()
}

func shouldUpdateDescription(plan types.String, state types.String) bool {
	if plan.IsUnknown() {
		return false
	}
	if plan.IsNull() {
		return !state.IsNull() && !state.IsUnknown() && state.ValueString() != ""
	}
	if state.IsNull() || state.IsUnknown() {
		return plan.ValueString() != ""
	}
	return plan.ValueString() != state.ValueString()
}

func shouldUpdateScope(plan types.String, state types.String) bool {
	if plan.IsNull() || plan.IsUnknown() {
		return false
	}
	if state.IsNull() || state.IsUnknown() {
		return true
	}
	return !strings.EqualFold(plan.ValueString(), state.ValueString())
}

func parseAutomationScope(value types.String) (*v1.AutomationScope, error) {
	if value.IsNull() || value.IsUnknown() {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(value.ValueString())) {
	case "user":
		scope := v1.AutomationScope_AUTOMATION_SCOPE_USER
		return &scope, nil
	case "team":
		scope := v1.AutomationScope_AUTOMATION_SCOPE_TEAM
		return &scope, nil
	case "team_editable":
		scope := v1.AutomationScope_AUTOMATION_SCOPE_TEAM_EDITABLE
		return &scope, nil
	case "team_editable_user":
		scope := v1.AutomationScope_AUTOMATION_SCOPE_TEAM_EDITABLE_USER
		return &scope, nil
	case "team_visible":
		scope := v1.AutomationScope_AUTOMATION_SCOPE_TEAM_VISIBLE
		return &scope, nil
	default:
		return nil, fmt.Errorf("invalid scope %q, must be \"user\", \"team\", \"team_visible\", \"team_editable_user\", or \"team_editable\"", value.ValueString())
	}
}

func automationScopeToModel(scope v1.AutomationScope) types.String {
	switch scope {
	case v1.AutomationScope_AUTOMATION_SCOPE_USER:
		return types.StringValue("user")
	case v1.AutomationScope_AUTOMATION_SCOPE_TEAM:
		return types.StringValue("team")
	case v1.AutomationScope_AUTOMATION_SCOPE_TEAM_EDITABLE:
		return types.StringValue("team_editable")
	case v1.AutomationScope_AUTOMATION_SCOPE_TEAM_EDITABLE_USER:
		return types.StringValue("team_editable_user")
	case v1.AutomationScope_AUTOMATION_SCOPE_TEAM_VISIBLE:
		return types.StringValue("team_visible")
	default:
		return types.StringNull()
	}
}

func readStringList(ctx context.Context, list types.List, fieldName string) ([]string, error) {
	if list.IsNull() || list.IsUnknown() {
		return nil, nil
	}

	var values []string
	diags := list.ElementsAs(ctx, &values, false)
	if diags.HasError() {
		return nil, fmt.Errorf("failed to read %s", fieldName)
	}
	return values, nil
}

// readNonBlankStringList is readStringList plus a check that no entry is empty
// or whitespace-only. Entries are rejected rather than trimmed so the value
// the API echoes back always matches the configured one.
func readNonBlankStringList(ctx context.Context, list types.List, fieldName string) ([]string, error) {
	values, err := readStringList(ctx, list, fieldName)
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s[%d] must not be empty", fieldName, i)
		}
	}
	return values, nil
}

// Preserve practitioner-supplied org casing when the API lowercases equivalent
// git_pull_request.orgs entries, avoiding post-apply state mismatches.
func preserveEquivalentGitPullRequestOrgs(ctx context.Context, state *platformWorkflowModel, reference platformWorkflowModel) {
	if state == nil {
		return
	}

	for i := 0; i < len(state.Triggers) && i < len(reference.Triggers); i++ {
		statePr := state.Triggers[i].GitPullRequest
		referencePr := reference.Triggers[i].GitPullRequest
		if statePr == nil || referencePr == nil {
			continue
		}
		if gitPullRequestOrgsEqualFold(ctx, statePr.Orgs, referencePr.Orgs) {
			statePr.Orgs = referencePr.Orgs
		}
	}
}

// Preserve practitioner-supplied environment_public_id formatting when the API
// returns an equivalent trimmed value, avoiding perpetual state/config diffs.
func preserveEquivalentEnvironmentPublicID(state *platformWorkflowModel, reference platformWorkflowModel) {
	if state == nil {
		return
	}
	if reference.EnvironmentPublicID.IsNull() || reference.EnvironmentPublicID.IsUnknown() {
		return
	}
	referenceValue := reference.EnvironmentPublicID.ValueString()

	// An empty/whitespace-only configured value is omitted from the API
	// request and comes back as null; keep the configured value so the state
	// matches the plan instead of drifting forever.
	if state.EnvironmentPublicID.IsNull() {
		if strings.TrimSpace(referenceValue) == "" {
			state.EnvironmentPublicID = reference.EnvironmentPublicID
		}
		return
	}
	if state.EnvironmentPublicID.IsUnknown() {
		return
	}

	if referenceValue == "" {
		return
	}
	if strings.TrimSpace(referenceValue) != state.EnvironmentPublicID.ValueString() {
		return
	}

	state.EnvironmentPublicID = reference.EnvironmentPublicID
}

// modelSelectionToProto mirrors the server's save-time checks that can be done
// without the catalog (validateStructuredModelSelectionForSave /
// validateModelSelection): non-empty model_id, no incomplete or duplicate
// parameters, and max_mode = false is not supported yet.
func modelSelectionToProto(ms *modelSelectionModel) (*v1.AutomationModelSelection, error) {
	if ms.ModelID.IsNull() || ms.ModelID.IsUnknown() || strings.TrimSpace(ms.ModelID.ValueString()) == "" {
		return nil, fmt.Errorf("model_selection.model_id is required")
	}
	selection := &v1.AutomationModelSelection{ModelId: strings.TrimSpace(ms.ModelID.ValueString())}

	seen := make(map[string]struct{}, len(ms.Parameters))
	for i, p := range ms.Parameters {
		if p.ID.IsUnknown() || p.Value.IsUnknown() {
			continue
		}
		id := strings.TrimSpace(p.ID.ValueString())
		value := strings.TrimSpace(p.Value.ValueString())
		if id == "" || value == "" {
			return nil, fmt.Errorf("model_selection.parameters[%d] must set both id and value", i)
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("model_selection.parameters contains duplicate parameter %q", id)
		}
		seen[id] = struct{}{}
		selection.Parameters = append(selection.Parameters, &v1.AutomationModelSelection_ParameterValue{Id: id, Value: value})
	}

	if !ms.MaxMode.IsNull() && !ms.MaxMode.IsUnknown() {
		if !ms.MaxMode.ValueBool() {
			return nil, fmt.Errorf("model_selection.max_mode = false is not supported by the server yet; omit it or set it to true")
		}
		maxMode := true
		selection.MaxMode = &maxMode
	}
	return selection, nil
}

func protoModelSelectionToModel(selection *v1.AutomationModelSelection) *modelSelectionModel {
	if selection == nil || strings.TrimSpace(selection.GetModelId()) == "" {
		return nil
	}
	ms := &modelSelectionModel{
		ModelID: types.StringValue(selection.GetModelId()),
		MaxMode: types.BoolNull(),
	}
	if selection.MaxMode != nil {
		ms.MaxMode = types.BoolValue(selection.GetMaxMode())
	}
	for _, p := range selection.GetParameters() {
		ms.Parameters = append(ms.Parameters, modelSelectionParameterModel{
			ID:    types.StringValue(p.GetId()),
			Value: types.StringValue(p.GetValue()),
		})
	}
	return ms
}

// Preserve the practitioner's model_selection spelling (model_id whitespace,
// parameter order and whitespace) when the server echoes an equivalent
// canonical selection, avoiding post-apply state mismatches.
func preserveEquivalentModelSelection(state *platformWorkflowModel, reference platformWorkflowModel) {
	if state == nil || state.ModelSelection == nil || reference.ModelSelection == nil {
		return
	}
	current, ref := state.ModelSelection, reference.ModelSelection
	if !ref.ModelID.IsNull() && !ref.ModelID.IsUnknown() &&
		strings.TrimSpace(ref.ModelID.ValueString()) == current.ModelID.ValueString() {
		current.ModelID = ref.ModelID
	}
	if ref.Parameters != nil && modelSelectionParametersEquivalent(current.Parameters, ref.Parameters) {
		current.Parameters = ref.Parameters
	}
}

func modelSelectionParametersEquivalent(current, reference []modelSelectionParameterModel) bool {
	if len(current) != len(reference) {
		return false
	}
	values := make(map[string]string, len(current))
	for _, p := range current {
		values[p.ID.ValueString()] = p.Value.ValueString()
	}
	for _, p := range reference {
		if p.ID.IsUnknown() || p.Value.IsUnknown() {
			return false
		}
		value, ok := values[strings.TrimSpace(p.ID.ValueString())]
		if !ok || value != strings.TrimSpace(p.Value.ValueString()) {
			return false
		}
	}
	return true
}

func gitPullRequestOrgsEqualFold(ctx context.Context, current, reference types.List) bool {
	if current.IsUnknown() || reference.IsUnknown() {
		return false
	}
	if current.IsNull() || reference.IsNull() {
		return current.IsNull() && reference.IsNull()
	}

	currentValues, err := readStringList(ctx, current, "git_pull_request.orgs")
	if err != nil {
		return false
	}
	referenceValues, err := readStringList(ctx, reference, "git_pull_request.orgs")
	if err != nil {
		return false
	}
	if len(currentValues) != len(referenceValues) {
		return false
	}
	for i := range currentValues {
		if !strings.EqualFold(currentValues[i], referenceValues[i]) {
			return false
		}
	}
	return true
}

// Preserve practitioner-supplied condition casing when it maps to the same
// API enum value, avoiding post-apply state mismatches.
func preserveEquivalentGitCICompletionConditions(state *platformWorkflowModel, reference platformWorkflowModel) {
	if state == nil {
		return
	}

	for i := 0; i < len(state.Triggers) && i < len(reference.Triggers); i++ {
		stateCI := state.Triggers[i].GitCICompleted
		referenceCI := reference.Triggers[i].GitCICompleted
		if stateCI == nil || referenceCI == nil {
			continue
		}
		if gitCICompletionConditionsEqualFold(stateCI.Condition, referenceCI.Condition) {
			stateCI.Condition = referenceCI.Condition
		}
	}
}

func gitCICompletionConditionsEqualFold(current, reference types.String) bool {
	if current.IsUnknown() || reference.IsUnknown() {
		return false
	}
	if current.IsNull() || reference.IsNull() {
		return current.IsNull() && reference.IsNull()
	}
	return strings.EqualFold(strings.TrimSpace(current.ValueString()), strings.TrimSpace(reference.ValueString()))
}

func validateAndNormalizeGitPullRequestTargets(rawOrgs, rawRepos []string) ([]string, []string, error) {
	normalizedOrgs := make([]string, 0, len(rawOrgs))
	seenOrgs := make(map[string]struct{}, len(rawOrgs))
	for _, rawOrg := range rawOrgs {
		normalizedOrg := strings.ToLower(strings.TrimSpace(rawOrg))
		if normalizedOrg == "" {
			continue
		}
		if strings.Contains(normalizedOrg, "/") {
			return nil, nil, fmt.Errorf("git_pull_request org %q must be a bare org name, not an owner/repo path", rawOrg)
		}
		if _, exists := seenOrgs[normalizedOrg]; exists {
			return nil, nil, fmt.Errorf("git_pull_request orgs contain a duplicate entry: %q", normalizedOrg)
		}
		seenOrgs[normalizedOrg] = struct{}{}
		normalizedOrgs = append(normalizedOrgs, normalizedOrg)
	}

	normalizedRepos := make([]string, 0, len(rawRepos))
	seenRepoKeys := make(map[string]struct{}, len(rawRepos))
	for _, rawRepo := range rawRepos {
		normalizedRepo := strings.TrimSpace(rawRepo)
		if normalizedRepo == "" {
			continue
		}

		repoKey := strings.ToLower(normalizedRepo)
		if repoName := parseGitRepoNameFromTarget(normalizedRepo); repoName != "" {
			repoKey = strings.ToLower(repoName)
		}
		if _, exists := seenRepoKeys[repoKey]; exists {
			return nil, nil, fmt.Errorf("git_pull_request repos contain a duplicate entry: %q", normalizedRepo)
		}
		seenRepoKeys[repoKey] = struct{}{}

		normalizedRepos = append(normalizedRepos, normalizedRepo)
	}

	if len(normalizedOrgs) > 0 && len(normalizedRepos) > 0 {
		return nil, nil, fmt.Errorf("git_pull_request cannot specify both repos and orgs; split mixed targets into separate triggers")
	}

	if len(normalizedOrgs) == 0 && len(normalizedRepos) == 0 {
		return nil, nil, fmt.Errorf("git_pull_request must specify at least one of orgs or repos")
	}

	return normalizedOrgs, normalizedRepos, nil
}

type gitRepoTargetMetadataValue struct {
	owner    string
	provider string
}

func gitRepoTargetMetadata(configuredRepo string) gitRepoTargetMetadataValue {
	metadata := gitRepoTargetMetadataValue{
		provider: "other",
	}

	trimmedRepo := strings.TrimSpace(configuredRepo)
	if trimmedRepo == "" {
		return metadata
	}

	repoName := parseGitRepoNameFromTarget(configuredRepo)
	if repoName != "" {
		separatorIndex := strings.Index(repoName, "/")
		if separatorIndex != -1 {
			metadata.owner = strings.ToLower(strings.TrimSpace(repoName[:separatorIndex]))
		}
	}

	if !strings.Contains(trimmedRepo, "://") {
		firstSlashIndex := strings.Index(trimmedRepo, "/")
		if firstSlashIndex != -1 {
			firstSegment := strings.TrimSpace(trimmedRepo[:firstSlashIndex])
			if provider := gitRepoProviderFromHostname(firstSegment); provider != "other" {
				metadata.provider = provider
				return metadata
			}
			// Known non-GitHub/GitLab hosts (e.g. bitbucket.org) should not be
			// treated as GitHub shorthands.
			if isKnownGitHostingDomain(firstSegment) {
				return metadata
			}
		}
		metadata.provider = "github"
		return metadata
	}

	parsedURL, err := url.Parse(trimmedRepo)
	if err != nil {
		return metadata
	}

	metadata.provider = gitRepoProviderFromHostname(parsedURL.Hostname())
	return metadata
}

func gitRepoOwnerFromTarget(configuredRepo string) string {
	return gitRepoTargetMetadata(configuredRepo).owner
}

func gitRepoTargetProvider(configuredRepo string) string {
	return gitRepoTargetMetadata(configuredRepo).provider
}

func gitRepoProviderFromHostname(hostname string) string {
	normalizedHostname := strings.ToLower(strings.TrimSpace(hostname))
	switch {
	case normalizedHostname == "gitlab.com",
		strings.HasSuffix(normalizedHostname, ".gitlab.com"),
		strings.HasPrefix(normalizedHostname, "gitlab."):
		return "gitlab"
	case normalizedHostname == "github.com",
		strings.HasSuffix(normalizedHostname, ".github.com"),
		strings.HasPrefix(normalizedHostname, "github."):
		return "github"
	default:
		return "other"
	}
}

func parseGitRepoNameFromTarget(configuredRepo string) string {
	trimmedRepo := strings.TrimSpace(configuredRepo)
	if trimmedRepo == "" {
		return ""
	}

	if strings.Contains(trimmedRepo, "://") {
		parsedURL, err := url.Parse(trimmedRepo)
		if err == nil {
			return extractOwnerRepoFromPath(parsedURL.Path)
		}
	}

	firstSlashIndex := strings.Index(trimmedRepo, "/")
	if firstSlashIndex == -1 {
		return ""
	}

	firstSegment := trimmedRepo[:firstSlashIndex]
	remainingPath := trimmedRepo[firstSlashIndex:]
	if isKnownGitHostingDomain(firstSegment) {
		return extractOwnerRepoFromPath(remainingPath)
	}

	return extractOwnerRepoFromPath("/" + trimmedRepo)
}

func extractOwnerRepoFromPath(path string) string {
	normalizedPath := strings.Trim(strings.TrimSpace(path), "/")
	normalizedPath = strings.TrimSuffix(normalizedPath, ".git")
	if normalizedPath == "" {
		return ""
	}

	parts := strings.Split(normalizedPath, "/")
	if len(parts) < 2 {
		return ""
	}

	owner := parts[0]
	repo := strings.Join(parts[1:], "/")
	if owner == "" || repo == "" {
		return ""
	}
	return owner + "/" + repo
}

func isKnownGitHostingDomain(hostname string) bool {
	lowerHostname := strings.ToLower(strings.TrimSpace(hostname))
	if lowerHostname == "" {
		return false
	}

	knownDomains := []string{
		"github.com",
		"gitlab.com",
		"bitbucket.org",
		"bitbucket.com",
		"codeberg.org",
		"gitea.com",
		"sr.ht",
	}
	for _, domain := range knownDomains {
		if lowerHostname == domain || strings.HasSuffix(lowerHostname, "."+domain) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Model → Proto conversion
// ---------------------------------------------------------------------------

func modelToWorkflow(ctx context.Context, m *platformWorkflowModel) (*v1.Workflow, error) {
	w := &v1.Workflow{}

	// Prompt
	prompt := &v1.Prompt{Prompt: m.Prompt.ValueString()}
	if !m.EffortLevel.IsNull() && !m.EffortLevel.IsUnknown() {
		switch m.EffortLevel.ValueString() {
		case "standard":
			prompt.EffortLevel = v1.PromptEffortLevel_PROMPT_EFFORT_LEVEL_STANDARD
		case "hard":
			prompt.EffortLevel = v1.PromptEffortLevel_PROMPT_EFFORT_LEVEL_HARD
		default:
			return nil, fmt.Errorf("invalid effort_level %q, must be \"standard\" or \"hard\"", m.EffortLevel.ValueString())
		}
	}
	w.Prompts = []*v1.Prompt{prompt}

	// Model / ModelSelection. When a selection is set the server derives the
	// slug from it, so the (possibly state-carried) slug is not sent.
	if m.ModelSelection != nil {
		selection, err := modelSelectionToProto(m.ModelSelection)
		if err != nil {
			return nil, err
		}
		w.ModelSelection = selection
	} else if !m.Model.IsNull() && !m.Model.IsUnknown() {
		model := m.Model.ValueString()
		w.Model = &model
	}

	// AgentOptions
	var agentOptions *v1.AgentOptions
	if !m.SkipInstall.IsNull() && !m.SkipInstall.IsUnknown() {
		skip := m.SkipInstall.ValueBool()
		agentOptions = &v1.AgentOptions{SkipInstall: &skip}
	}
	if !m.EnvironmentPublicID.IsNull() && !m.EnvironmentPublicID.IsUnknown() {
		environmentPublicID := strings.TrimSpace(m.EnvironmentPublicID.ValueString())
		if environmentPublicID != "" {
			if agentOptions == nil {
				agentOptions = &v1.AgentOptions{}
			}
			agentOptions.EnvironmentPublicId = &environmentPublicID
		}
	}
	if m.PrivateWorker != nil {
		if agentOptions == nil {
			agentOptions = &v1.AgentOptions{}
		}

		privateWorker := &v1.AgentPrivateWorkerConfig{}
		if !m.PrivateWorker.Labels.IsNull() && !m.PrivateWorker.Labels.IsUnknown() {
			labels := make(map[string]string)
			diags := m.PrivateWorker.Labels.ElementsAs(ctx, &labels, false)
			if diags.HasError() {
				return nil, fmt.Errorf("failed to read private_worker.labels")
			}

			keys := make([]string, 0, len(labels))
			for key := range labels {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				privateWorker.Labels = append(privateWorker.Labels, &v1.AgentPrivateWorkerLabel{
					Key:   key,
					Value: labels[key],
				})
			}
		}
		agentOptions.PrivateWorker = privateWorker
	}
	if agentOptions != nil {
		w.AgentOptions = agentOptions
	}

	// MemoryEnabled
	if !m.MemoryEnabled.IsNull() && !m.MemoryEnabled.IsUnknown() {
		enabled := m.MemoryEnabled.ValueBool()
		w.MemoryEnabled = &enabled
	}

	// DisabledDefaultTools
	disabledTools, err := readStringList(ctx, m.DisabledDefaultTools, "disabled_default_tools")
	if err != nil {
		return nil, err
	}
	for _, tool := range disabledTools {
		parsed, err := parseAutomationDefaultTool(tool)
		if err != nil {
			return nil, err
		}
		w.DisabledDefaultTools = append(w.DisabledDefaultTools, parsed)
	}

	// Triggers
	for i, t := range m.Triggers {
		trigger, err := triggerModelToProto(ctx, &t)
		if err != nil {
			return nil, fmt.Errorf("trigger[%d]: %w", i, err)
		}
		w.Triggers = append(w.Triggers, trigger)
	}

	// GitConfig: the explicit git_repo/git_branch attributes plus the repos
	// referenced by git triggers. Without the trigger-derived repos, an
	// automation whose only repo references live in its triggers would be
	// persisted with no git configuration and could never launch.
	gitRepo := ""
	if !m.GitRepo.IsNull() && !m.GitRepo.IsUnknown() {
		gitRepo = m.GitRepo.ValueString()
	}
	gitBranch := ""
	if !m.GitBranch.IsNull() && !m.GitBranch.IsUnknown() {
		gitBranch = m.GitBranch.ValueString()
	}
	repos := gitConfigRepos(gitRepo, w.Triggers)
	if gitRepo != "" || gitBranch != "" || len(repos) > 0 {
		w.GitConfig = &v1.GitConfig{
			Repo:   gitRepo,
			Branch: gitBranch,
			Repos:  repos,
		}
	}

	// Actions
	for i, a := range m.Actions {
		action, err := actionModelToProto(&a)
		if err != nil {
			return nil, fmt.Errorf("action[%d]: %w", i, err)
		}
		w.Actions = append(w.Actions, action)
	}

	return w, nil
}

// gitConfigRepos merges the top-level git_repo (first, when set) with the
// repos referenced by the workflow's git triggers, preserving order and
// de-duplicating equivalent targets. Org-scoped pull request triggers name a
// whole org rather than concrete repos, so they contribute nothing here.
func gitConfigRepos(gitRepo string, triggers []*v1.Trigger) []string {
	var repos []string
	seen := make(map[string]struct{})
	add := func(repo string) {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			return
		}
		key := gitConfigRepoKey(repo)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		repos = append(repos, repo)
	}

	add(gitRepo)
	for _, t := range triggers {
		git := t.GetGit()
		if git == nil {
			continue
		}
		if pr := git.GetPullRequest(); pr != nil {
			for _, repo := range pr.GetRepos() {
				add(repo)
			}
			add(pr.GetRepo())
		}
		if push := git.GetPush(); push != nil {
			add(push.GetRepo())
		}
	}
	return repos
}

// gitConfigRepoKey returns a case-insensitive de-duplication key for a repo
// target, treating different spellings of the same owner/repo on the same host
// (e.g. with and without a GitHub host prefix) as equivalent.
func gitConfigRepoKey(repo string) string {
	if name := parseGitRepoNameFromTarget(repo); name != "" {
		return gitConfigRepoKeyHost(repo) + ":" + strings.ToLower(name)
	}
	return strings.ToLower(repo)
}

func gitConfigRepoKeyHost(repo string) string {
	trimmedRepo := strings.TrimSpace(repo)
	if strings.Contains(trimmedRepo, "://") {
		parsedURL, err := url.Parse(trimmedRepo)
		if err == nil && parsedURL.Hostname() != "" {
			return strings.ToLower(parsedURL.Hostname())
		}
	}

	firstSlashIndex := strings.Index(trimmedRepo, "/")
	if firstSlashIndex != -1 {
		firstSegment := strings.TrimSpace(trimmedRepo[:firstSlashIndex])
		if isKnownGitHostingDomain(firstSegment) || gitRepoProviderFromHostname(firstSegment) != "other" {
			return strings.ToLower(firstSegment)
		}
	}

	return "github.com"
}

func actionModelToProto(a *actionModel) (*v1.Action, error) {
	// Exactly one action type should be set
	count := 0
	if a.PrComment != nil {
		count++
	}
	if a.GitPr != nil {
		count++
	}
	if a.RequestReviewers != nil {
		count++
	}
	if a.Mcp != nil {
		count++
	}
	if a.Slack != nil {
		count++
	}
	if a.ReadSlack != nil {
		count++
	}
	if a.MicrosoftTeams != nil {
		count++
	}
	if a.ReadMicrosoftTeams != nil {
		count++
	}
	if a.ManageCheckRun != nil {
		count++
	}
	if a.ApprovePr != nil {
		count++
	}
	if a.ResolveReviewThreads != nil {
		count++
	}
	if count != 1 {
		return nil, fmt.Errorf("must specify exactly one of %s", actionTypeNames)
	}

	if a.PrComment != nil {
		prComment := &v1.PrCommentAction{}
		if !a.PrComment.AllowInlineComments.IsNull() && !a.PrComment.AllowInlineComments.IsUnknown() {
			prComment.AllowInlineComments = a.PrComment.AllowInlineComments.ValueBool()
		}
		if !a.PrComment.AllowApprove.IsNull() && !a.PrComment.AllowApprove.IsUnknown() {
			prComment.AllowApprove = a.PrComment.AllowApprove.ValueBool()
		}
		return &v1.Action{Action: &v1.Action_PrComment{PrComment: prComment}}, nil
	}
	if a.GitPr != nil {
		return &v1.Action{Action: &v1.Action_GitPr{GitPr: &v1.GitPrAction{}}}, nil
	}
	if a.RequestReviewers != nil {
		return &v1.Action{Action: &v1.Action_RequestReviewers{RequestReviewers: &v1.RequestReviewersAction{}}}, nil
	}
	if a.Mcp != nil {
		server := &v1.McpServerConfig{Name: a.Mcp.Server.ValueString()}
		if !a.Mcp.ServerID.IsNull() && !a.Mcp.ServerID.IsUnknown() {
			id := a.Mcp.ServerID.ValueInt64()
			server.Id = &id
		}
		return &v1.Action{
			Action: &v1.Action_Mcp{
				Mcp: &v1.McpAction{
					Server: server,
				},
			},
		}, nil
	}
	if a.Slack != nil {
		slack := &v1.SlackAction{}
		if !a.Slack.Channel.IsNull() && !a.Slack.Channel.IsUnknown() {
			slack.Channel = a.Slack.Channel.ValueString()
		}
		if !a.Slack.Generalized.IsNull() && !a.Slack.Generalized.IsUnknown() {
			slack.Generalized = a.Slack.Generalized.ValueBool()
		}
		if !a.Slack.RespondInThread.IsNull() && !a.Slack.RespondInThread.IsUnknown() {
			slack.RespondInThread = a.Slack.RespondInThread.ValueBool()
		}
		if !a.Slack.PostAsThread.IsNull() && !a.Slack.PostAsThread.IsUnknown() {
			slack.PostAsThread = a.Slack.PostAsThread.ValueBool()
		}
		return &v1.Action{Action: &v1.Action_Slack{Slack: slack}}, nil
	}
	if a.ReadSlack != nil {
		return &v1.Action{Action: &v1.Action_ReadSlack{ReadSlack: &v1.ReadSlackAction{}}}, nil
	}
	if a.MicrosoftTeams != nil {
		teams := &v1.MicrosoftTeamsAction{}
		if !a.MicrosoftTeams.TenantID.IsNull() && !a.MicrosoftTeams.TenantID.IsUnknown() {
			teams.TenantId = a.MicrosoftTeams.TenantID.ValueString()
		}
		if !a.MicrosoftTeams.TeamID.IsNull() && !a.MicrosoftTeams.TeamID.IsUnknown() {
			teams.TeamId = a.MicrosoftTeams.TeamID.ValueString()
		}
		if !a.MicrosoftTeams.ChannelID.IsNull() && !a.MicrosoftTeams.ChannelID.IsUnknown() {
			teams.ChannelId = a.MicrosoftTeams.ChannelID.ValueString()
		}
		if !a.MicrosoftTeams.ChannelIDs.IsNull() && !a.MicrosoftTeams.ChannelIDs.IsUnknown() {
			var channelIDs []string
			diags := a.MicrosoftTeams.ChannelIDs.ElementsAs(context.Background(), &channelIDs, false)
			if diags.HasError() {
				return nil, fmt.Errorf("failed to read microsoft_teams.channel_ids")
			}
			teams.ChannelIds = channelIDs
		}
		if !a.MicrosoftTeams.Generalized.IsNull() && !a.MicrosoftTeams.Generalized.IsUnknown() {
			teams.Generalized = a.MicrosoftTeams.Generalized.ValueBool()
		}
		if !a.MicrosoftTeams.RespondInThread.IsNull() && !a.MicrosoftTeams.RespondInThread.IsUnknown() {
			teams.RespondInThread = a.MicrosoftTeams.RespondInThread.ValueBool()
		}
		if !a.MicrosoftTeams.PostAsThread.IsNull() && !a.MicrosoftTeams.PostAsThread.IsUnknown() {
			teams.PostAsThread = a.MicrosoftTeams.PostAsThread.ValueBool()
		}
		if teams.RespondInThread && teams.PostAsThread {
			return nil, fmt.Errorf("microsoft_teams cannot set both respond_in_thread and post_as_thread")
		}
		return &v1.Action{Action: &v1.Action_MicrosoftTeams{MicrosoftTeams: teams}}, nil
	}
	if a.ReadMicrosoftTeams != nil {
		return &v1.Action{Action: &v1.Action_ReadMicrosoftTeams{ReadMicrosoftTeams: &v1.ReadMicrosoftTeamsAction{}}}, nil
	}
	if a.ManageCheckRun != nil {
		return &v1.Action{Action: &v1.Action_ManageCheckRun{ManageCheckRun: &v1.ManageCheckRunAction{}}}, nil
	}
	if a.ApprovePr != nil {
		return &v1.Action{Action: &v1.Action_ApprovePr{ApprovePr: &v1.ApprovePrAction{}}}, nil
	}
	if a.ResolveReviewThreads != nil {
		return &v1.Action{Action: &v1.Action_ResolveReviewThreads{ResolveReviewThreads: &v1.ResolveReviewThreadsAction{}}}, nil
	}

	return nil, fmt.Errorf("no action type specified")
}

func triggerModelToProto(ctx context.Context, t *triggerModel) (*v1.Trigger, error) {
	trigger := &v1.Trigger{}

	// Exactly one trigger type should be set
	count := 0
	if t.GitPullRequest != nil {
		count++
	}
	if t.GitPush != nil {
		count++
	}
	if t.GitCICompleted != nil {
		count++
	}
	if t.Cron != nil {
		count++
	}
	if t.Slack != nil {
		count++
	}
	if t.Linear != nil {
		count++
	}
	if t.Webhook != nil {
		count++
	}
	if t.MicrosoftTeams != nil {
		count++
	}
	if t.MicrosoftTeamsChannelCreated != nil {
		count++
	}
	if t.SlackChannelCreated != nil {
		count++
	}
	if t.SlackReactionAdded != nil {
		count++
	}
	if t.SlackMention != nil {
		count++
	}
	if t.SlackAnyReactionAdded != nil {
		count++
	}
	if t.PagerDuty != nil {
		count++
	}
	if t.Sentry != nil {
		count++
	}
	if count != 1 {
		return nil, fmt.Errorf("must specify exactly one of %s", triggerTypeNames)
	}

	// Git pull request
	if pr := t.GitPullRequest; pr != nil {
		orgs, err := readStringList(ctx, pr.Orgs, "orgs")
		if err != nil {
			return nil, err
		}
		repos, err := readStringList(ctx, pr.Repos, "repos")
		if err != nil {
			return nil, err
		}
		orgs, repos, err = validateAndNormalizeGitPullRequestTargets(orgs, repos)
		if err != nil {
			return nil, err
		}
		event := &v1.GitPullRequestEvent{
			Orgs:  orgs,
			Repos: repos,
		}
		if !pr.IgnoreDraftPrs.IsNull() && !pr.IgnoreDraftPrs.IsUnknown() {
			event.IgnoreDraftPrs = pr.IgnoreDraftPrs.ValueBool()
		}
		if !pr.PrAction.IsNull() && !pr.PrAction.IsUnknown() {
			action, err := parsePrAction(pr.PrAction.ValueString())
			if err != nil {
				return nil, err
			}
			event.PrAction = action
		}
		if !pr.CommentContains.IsNull() && !pr.CommentContains.IsUnknown() {
			event.CommentContains = pr.CommentContains.ValueString()
		}
		if !pr.CommentContainsIsRegex.IsNull() && !pr.CommentContainsIsRegex.IsUnknown() {
			event.CommentContainsIsRegex = pr.CommentContainsIsRegex.ValueBool()
		}

		gitTrigger := &v1.GitTrigger{
			Event: &v1.GitTrigger_PullRequest{PullRequest: event},
		}
		if !t.UserAllowlist.IsNull() && !t.UserAllowlist.IsUnknown() {
			var users []string
			diags := t.UserAllowlist.ElementsAs(ctx, &users, false)
			if diags.HasError() {
				return nil, fmt.Errorf("failed to read user_allowlist")
			}
			gitTrigger.UserAllowlist = users
		}
		trigger.Trigger = &v1.Trigger_Git{Git: gitTrigger}
	}

	// Git push
	if push := t.GitPush; push != nil {
		event := &v1.GitPushEvent{Repo: push.Repo.ValueString()}
		if !push.Branch.IsNull() && !push.Branch.IsUnknown() {
			event.Branch = push.Branch.ValueString()
		}
		gitTrigger := &v1.GitTrigger{
			Event: &v1.GitTrigger_Push{Push: event},
		}
		if !t.UserAllowlist.IsNull() && !t.UserAllowlist.IsUnknown() {
			var users []string
			diags := t.UserAllowlist.ElementsAs(ctx, &users, false)
			if diags.HasError() {
				return nil, fmt.Errorf("failed to read user_allowlist")
			}
			gitTrigger.UserAllowlist = users
		}
		trigger.Trigger = &v1.Trigger_Git{Git: gitTrigger}
	}

	// Git CI completed
	if ci := t.GitCICompleted; ci != nil {
		repos, err := readStringList(ctx, ci.Repos, "git_ci_completed.repos")
		if err != nil {
			return nil, err
		}
		normalizedRepos := make([]string, 0, len(repos))
		for _, repo := range repos {
			normalizedRepo := strings.TrimSpace(repo)
			if normalizedRepo != "" {
				normalizedRepos = append(normalizedRepos, normalizedRepo)
			}
		}
		repos = normalizedRepos
		if len(repos) == 0 {
			return nil, fmt.Errorf("git_ci_completed must specify at least one repo")
		}
		event := &v1.GitCICompletedEvent{Repos: repos}
		if !ci.Condition.IsNull() && !ci.Condition.IsUnknown() {
			condition, err := parseCICompletionCondition(ci.Condition.ValueString())
			if err != nil {
				return nil, err
			}
			event.Condition = condition
		}
		if !ci.IgnoreBaseFailures.IsNull() && !ci.IgnoreBaseFailures.IsUnknown() {
			event.IgnoreBaseFailures = ci.IgnoreBaseFailures.ValueBool()
		}
		if !ci.Branch.IsNull() && !ci.Branch.IsUnknown() {
			event.Branch = ci.Branch.ValueString()
		}
		gitTrigger := &v1.GitTrigger{
			Event: &v1.GitTrigger_CiCompleted{CiCompleted: event},
		}
		if !t.UserAllowlist.IsNull() && !t.UserAllowlist.IsUnknown() {
			var users []string
			diags := t.UserAllowlist.ElementsAs(ctx, &users, false)
			if diags.HasError() {
				return nil, fmt.Errorf("failed to read user_allowlist")
			}
			gitTrigger.UserAllowlist = users
		}
		trigger.Trigger = &v1.Trigger_Git{Git: gitTrigger}
	}

	// Cron
	if cron := t.Cron; cron != nil {
		trigger.Trigger = &v1.Trigger_Cron{
			Cron: &v1.CronTrigger{Cron: cron.Schedule.ValueString()},
		}
	}

	// Slack
	if slack := t.Slack; slack != nil {
		st := &v1.SlackTrigger{Channel: slack.Channel.ValueString()}
		if !slack.MessageContains.IsNull() && !slack.MessageContains.IsUnknown() {
			st.MessageContains = slack.MessageContains.ValueString()
		}
		if !slack.MessageContainsIsRegex.IsNull() && !slack.MessageContainsIsRegex.IsUnknown() {
			st.MessageContainsIsRegex = slack.MessageContainsIsRegex.ValueBool()
		}
		if !slack.BlockUnauthenticatedSlackUsers.IsNull() && !slack.BlockUnauthenticatedSlackUsers.IsUnknown() && slack.BlockUnauthenticatedSlackUsers.ValueBool() {
			st.BlockUnauthenticatedSlackUsers = true
		}
		if err := applySlackCompletionReaction(st, slack.CompletionReactionMode, slack.CompletionReactionCustomEmoji); err != nil {
			return nil, err
		}
		trigger.Trigger = &v1.Trigger_SlackTrigger{SlackTrigger: st}
	}

	// Slack channel created
	if scc := t.SlackChannelCreated; scc != nil {
		st := &v1.SlackChannelCreatedTrigger{}
		if !scc.ChannelNameContains.IsNull() && !scc.ChannelNameContains.IsUnknown() {
			st.ChannelNameContains = scc.ChannelNameContains.ValueString()
		}
		trigger.Trigger = &v1.Trigger_SlackChannelCreated{SlackChannelCreated: st}
	}

	// Slack reaction added
	if sra := t.SlackReactionAdded; sra != nil {
		channel, err := requiredSlackChannel(sra.Channel, "slack_reaction_added")
		if err != nil {
			return nil, err
		}
		emojiName, err := validateSlackEmojiShortName(sra.EmojiName)
		if err != nil {
			return nil, err
		}
		st := &v1.SlackReactionAddedTrigger{
			Channel:                        channel,
			EmojiName:                      emojiName,
			BlockUnauthenticatedSlackUsers: boolIsTrue(sra.BlockUnauthenticatedSlackUsers),
			OnlyOwnerReactions:             boolIsTrue(sra.OnlyOwnerReactions),
		}
		trigger.Trigger = &v1.Trigger_SlackReactionAdded{SlackReactionAdded: st}
	}

	// Slack mention
	if sm := t.SlackMention; sm != nil {
		channel, err := requiredSlackChannel(sm.Channel, "slack_mention")
		if err != nil {
			return nil, err
		}
		st := &v1.SlackMentionTrigger{
			Channel:                        channel,
			BlockUnauthenticatedSlackUsers: boolIsTrue(sm.BlockUnauthenticatedSlackUsers),
		}
		trigger.Trigger = &v1.Trigger_SlackMention{SlackMention: st}
	}

	// Slack any reaction added
	if sar := t.SlackAnyReactionAdded; sar != nil {
		channel, err := requiredSlackChannel(sar.Channel, "slack_any_reaction_added")
		if err != nil {
			return nil, err
		}
		st := &v1.SlackAnyReactionAddedTrigger{
			Channel:                        channel,
			BlockUnauthenticatedSlackUsers: boolIsTrue(sar.BlockUnauthenticatedSlackUsers),
			OnlyOwnerReactions:             boolIsTrue(sar.OnlyOwnerReactions),
		}
		trigger.Trigger = &v1.Trigger_SlackAnyReactionAdded{SlackAnyReactionAdded: st}
	}

	// PagerDuty
	if pd := t.PagerDuty; pd != nil {
		pt := &v1.PagerDutyTrigger{}
		serviceIDs, err := readNonBlankStringList(ctx, pd.ServiceIDs, "pagerduty.service_ids")
		if err != nil {
			return nil, err
		}
		pt.ServiceIds = serviceIDs

		eventCount := 0
		if pd.IncidentTriggered != nil {
			eventCount++
			pt.Event = &v1.PagerDutyTrigger_IncidentTriggered{IncidentTriggered: &v1.PagerDutyIncidentTriggeredEvent{}}
		}
		if pd.IncidentAcknowledged != nil {
			eventCount++
			pt.Event = &v1.PagerDutyTrigger_IncidentAcknowledged{IncidentAcknowledged: &v1.PagerDutyIncidentAcknowledgedEvent{}}
		}
		if pd.IncidentResolved != nil {
			eventCount++
			pt.Event = &v1.PagerDutyTrigger_IncidentResolved{IncidentResolved: &v1.PagerDutyIncidentResolvedEvent{}}
		}
		if pd.IncidentEscalated != nil {
			eventCount++
			pt.Event = &v1.PagerDutyTrigger_IncidentEscalated{IncidentEscalated: &v1.PagerDutyIncidentEscalatedEvent{}}
		}
		if pd.IncidentAny != nil {
			eventCount++
			pt.Event = &v1.PagerDutyTrigger_IncidentAny{IncidentAny: &v1.PagerDutyIncidentAnyEvent{}}
		}
		if eventCount != 1 {
			return nil, fmt.Errorf("pagerduty trigger must specify exactly one of incident_triggered, incident_acknowledged, incident_resolved, incident_escalated, or incident_any")
		}
		trigger.Trigger = &v1.Trigger_Pagerduty{Pagerduty: pt}
	}

	// Sentry
	if sentry := t.Sentry; sentry != nil {
		st := &v1.SentryTrigger{}
		projectIDs, err := readNonBlankStringList(ctx, sentry.ProjectIDs, "sentry.project_ids")
		if err != nil {
			return nil, err
		}
		st.ProjectIds = projectIDs

		eventCount := 0
		if sentry.IssueCreated != nil {
			eventCount++
			st.Event = &v1.SentryTrigger_IssueCreated{IssueCreated: &v1.SentryIssueCreatedEvent{}}
		}
		if sentry.IssueResolved != nil {
			eventCount++
			st.Event = &v1.SentryTrigger_IssueResolved{IssueResolved: &v1.SentryIssueResolvedEvent{}}
		}
		if sentry.IssueAssigned != nil {
			eventCount++
			st.Event = &v1.SentryTrigger_IssueAssigned{IssueAssigned: &v1.SentryIssueAssignedEvent{}}
		}
		if sentry.IssueArchived != nil {
			eventCount++
			st.Event = &v1.SentryTrigger_IssueArchived{IssueArchived: &v1.SentryIssueArchivedEvent{}}
		}
		if sentry.IssueUnresolved != nil {
			eventCount++
			st.Event = &v1.SentryTrigger_IssueUnresolved{IssueUnresolved: &v1.SentryIssueUnresolvedEvent{}}
		}
		if sentry.IssueAny != nil {
			eventCount++
			st.Event = &v1.SentryTrigger_IssueAny{IssueAny: &v1.SentryIssueAnyEvent{}}
		}
		if eventCount != 1 {
			return nil, fmt.Errorf("sentry trigger must specify exactly one of issue_created, issue_resolved, issue_assigned, issue_archived, issue_unresolved, or issue_any")
		}
		trigger.Trigger = &v1.Trigger_Sentry{Sentry: st}
	}

	// Linear
	if linear := t.Linear; linear != nil {
		lt := &v1.LinearTrigger{}

		if !linear.ProjectIDs.IsNull() && !linear.ProjectIDs.IsUnknown() {
			var projectIDs []string
			diags := linear.ProjectIDs.ElementsAs(ctx, &projectIDs, false)
			if diags.HasError() {
				return nil, fmt.Errorf("failed to read linear.project_ids")
			}
			lt.ProjectIds = projectIDs
		}
		if !linear.TeamIDs.IsNull() && !linear.TeamIDs.IsUnknown() {
			var teamIDs []string
			diags := linear.TeamIDs.ElementsAs(ctx, &teamIDs, false)
			if diags.HasError() {
				return nil, fmt.Errorf("failed to read linear.team_ids")
			}
			lt.TeamIds = teamIDs
		}

		eventCount := 0
		if linear.IssueCreated != nil {
			eventCount++
			lt.Event = &v1.LinearTrigger_IssueCreated{
				IssueCreated: &v1.LinearIssueCreatedEvent{},
			}
		}
		if linear.StatusChanged != nil {
			eventCount++
			statusChanged := &v1.LinearStatusChangedEvent{}
			if !linear.StatusChanged.StatusIDs.IsNull() && !linear.StatusChanged.StatusIDs.IsUnknown() {
				var statusIDs []string
				diags := linear.StatusChanged.StatusIDs.ElementsAs(ctx, &statusIDs, false)
				if diags.HasError() {
					return nil, fmt.Errorf("failed to read linear.status_changed.status_ids")
				}
				statusChanged.StatusIds = statusIDs
			}
			lt.Event = &v1.LinearTrigger_StatusChanged{StatusChanged: statusChanged}
		}
		if linear.EndOfCycle != nil {
			eventCount++
			endOfCycle := &v1.LinearEndOfCycleEvent{}
			if !linear.EndOfCycle.CycleIDs.IsNull() && !linear.EndOfCycle.CycleIDs.IsUnknown() {
				var cycleIDs []string
				diags := linear.EndOfCycle.CycleIDs.ElementsAs(ctx, &cycleIDs, false)
				if diags.HasError() {
					return nil, fmt.Errorf("failed to read linear.end_of_cycle.cycle_ids")
				}
				endOfCycle.CycleIds = cycleIDs
			}
			lt.Event = &v1.LinearTrigger_EndOfCycle{EndOfCycle: endOfCycle}
		}

		if eventCount == 0 {
			return nil, fmt.Errorf("linear trigger must specify exactly one of issue_created, status_changed, or end_of_cycle")
		}
		if eventCount > 1 {
			return nil, fmt.Errorf("linear trigger must specify exactly one of issue_created, status_changed, or end_of_cycle")
		}

		trigger.Trigger = &v1.Trigger_Linear{Linear: lt}
	}

	// Webhook
	if t.Webhook != nil {
		trigger.Trigger = &v1.Trigger_Webhook{Webhook: &v1.WebhookTrigger{}}
	}

	// Microsoft Teams (channel message)
	if teams := t.MicrosoftTeams; teams != nil {
		mt := &v1.MicrosoftTeamsTrigger{
			TenantId: teams.TenantID.ValueString(),
		}
		if !teams.TeamID.IsNull() && !teams.TeamID.IsUnknown() {
			mt.TeamId = teams.TeamID.ValueString()
		}
		if !teams.TeamIDs.IsNull() && !teams.TeamIDs.IsUnknown() {
			var teamIDs []string
			diags := teams.TeamIDs.ElementsAs(ctx, &teamIDs, false)
			if diags.HasError() {
				return nil, fmt.Errorf("failed to read microsoft_teams.team_ids")
			}
			mt.TeamIds = teamIDs
		}
		if !teams.ChannelIDs.IsNull() && !teams.ChannelIDs.IsUnknown() {
			var channelIDs []string
			diags := teams.ChannelIDs.ElementsAs(ctx, &channelIDs, false)
			if diags.HasError() {
				return nil, fmt.Errorf("failed to read microsoft_teams.channel_ids")
			}
			mt.ChannelIds = channelIDs
		}
		if !teams.MessageContains.IsNull() && !teams.MessageContains.IsUnknown() {
			mt.MessageContains = teams.MessageContains.ValueString()
		}
		if !teams.MessageContainsIsRegex.IsNull() && !teams.MessageContainsIsRegex.IsUnknown() {
			mt.MessageContainsIsRegex = teams.MessageContainsIsRegex.ValueBool()
		}
		if !teams.BlockUnauthenticatedTeamsUsers.IsNull() && !teams.BlockUnauthenticatedTeamsUsers.IsUnknown() {
			mt.BlockUnauthenticatedTeamsUsers = teams.BlockUnauthenticatedTeamsUsers.ValueBool()
		}
		trigger.Trigger = &v1.Trigger_MicrosoftTeamsTrigger{MicrosoftTeamsTrigger: mt}
	}

	// Microsoft Teams (channel created)
	if mtc := t.MicrosoftTeamsChannelCreated; mtc != nil {
		mctt := &v1.MicrosoftTeamsChannelCreatedTrigger{
			TenantId: mtc.TenantID.ValueString(),
		}
		teamIDs, err := readNonBlankStringList(ctx, mtc.TeamIDs, "microsoft_teams_channel_created.team_ids")
		if err != nil {
			return nil, err
		}
		if len(teamIDs) == 0 {
			return nil, fmt.Errorf("microsoft_teams_channel_created must specify at least one team_id")
		}
		mctt.TeamIds = teamIDs
		if !mtc.ChannelNameContains.IsNull() && !mtc.ChannelNameContains.IsUnknown() {
			mctt.ChannelNameContains = mtc.ChannelNameContains.ValueString()
		}
		trigger.Trigger = &v1.Trigger_MicrosoftTeamsChannelCreated{MicrosoftTeamsChannelCreated: mctt}
	}

	return trigger, nil
}

func boolIsTrue(value types.Bool) bool {
	return !value.IsNull() && !value.IsUnknown() && value.ValueBool()
}

// boolOrNull mirrors the existing slack trigger convention of reading proto3
// booleans back as null when false so unset configs do not drift.
func boolOrNull(value bool) types.Bool {
	if value {
		return types.BoolValue(true)
	}
	return types.BoolNull()
}

func stringOrNull(value string) types.String {
	if value == "" {
		return types.StringNull()
	}
	return types.StringValue(value)
}

func requiredSlackChannel(value types.String, block string) (string, error) {
	if value.IsNull() || value.IsUnknown() || strings.TrimSpace(value.ValueString()) == "" {
		return "", fmt.Errorf("%s.channel is required", block)
	}
	return strings.TrimSpace(value.ValueString()), nil
}

// validateSlackEmojiShortName requires the canonical form the server stores
// (lowercase short name, no colons or skin-tone suffix) so the value reads
// back unchanged instead of drifting after the server normalises it.
func validateSlackEmojiShortName(value types.String) (string, error) {
	if value.IsNull() || value.IsUnknown() || strings.TrimSpace(value.ValueString()) == "" {
		return "", fmt.Errorf("slack_reaction_added.emoji_name is required")
	}
	name := value.ValueString()
	for _, r := range name {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if !isLower && !isDigit && r != '_' && r != '+' && r != '-' {
			return "", fmt.Errorf("invalid slack_reaction_added.emoji_name %q: use the lowercase Slack short name without colons (e.g. \"thumbsup\")", name)
		}
	}
	return name, nil
}

func parseAutomationDefaultTool(s string) (v1.AutomationDefaultTool, error) {
	// Exact match only: the value is read back as the canonical lowercase name,
	// so accepting other spellings would drift after apply.
	switch s {
	case "open_git_pr":
		return v1.AutomationDefaultTool_AUTOMATION_DEFAULT_TOOL_OPEN_GIT_PR, nil
	default:
		return 0, fmt.Errorf("invalid disabled_default_tools entry %q, must be \"open_git_pr\"", s)
	}
}

func automationDefaultToolToString(tool v1.AutomationDefaultTool) string {
	switch tool {
	case v1.AutomationDefaultTool_AUTOMATION_DEFAULT_TOOL_OPEN_GIT_PR:
		return "open_git_pr"
	default:
		return ""
	}
}

// applySlackCompletionReaction translates the Terraform completion reaction
// fields onto a SlackTrigger proto, validating the relationship between the
// mode and the custom emoji.
func applySlackCompletionReaction(st *v1.SlackTrigger, mode types.String, customEmoji types.String) error {
	hasMode := !mode.IsNull() && !mode.IsUnknown()
	hasEmoji := !customEmoji.IsNull() && !customEmoji.IsUnknown() && customEmoji.ValueString() != ""

	if !hasMode {
		if hasEmoji {
			return fmt.Errorf("slack.completion_reaction_custom_emoji can only be set when slack.completion_reaction_mode is \"custom\"")
		}
		return nil
	}

	parsedMode, err := parseSlackCompletionReactionMode(mode.ValueString())
	if err != nil {
		return err
	}

	if parsedMode == v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_CUSTOM {
		if !hasEmoji {
			return fmt.Errorf("slack.completion_reaction_custom_emoji is required when slack.completion_reaction_mode is \"custom\"")
		}
	} else if hasEmoji {
		return fmt.Errorf("slack.completion_reaction_custom_emoji can only be set when slack.completion_reaction_mode is \"custom\"")
	}

	st.SlackCompletionReactionMode = &parsedMode
	if hasEmoji {
		emoji := customEmoji.ValueString()
		st.SlackCompletionReactionCustomEmoji = &emoji
	}
	return nil
}

func parseSlackCompletionReactionMode(s string) (v1.SlackCompletionReactionMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on":
		return v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_ON, nil
	case "off":
		return v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_OFF, nil
	case "custom":
		return v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_CUSTOM, nil
	default:
		return 0, fmt.Errorf("invalid slack.completion_reaction_mode %q, must be \"on\", \"off\", or \"custom\"", s)
	}
}

func slackCompletionReactionModeToString(m v1.SlackCompletionReactionMode) string {
	switch m {
	case v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_ON:
		return "on"
	case v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_OFF:
		return "off"
	case v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_CUSTOM:
		return "custom"
	default:
		return ""
	}
}

func parsePrAction(s string) (v1.GitPullRequestAction, error) {
	switch s {
	case "opened":
		return v1.GitPullRequestAction_GIT_PULL_REQUEST_ACTION_OPENED, nil
	case "pushed":
		return v1.GitPullRequestAction_GIT_PULL_REQUEST_ACTION_PUSHED, nil
	case "merged":
		return v1.GitPullRequestAction_GIT_PULL_REQUEST_ACTION_MERGED, nil
	case "commented":
		return v1.GitPullRequestAction_GIT_PULL_REQUEST_ACTION_COMMENTED, nil
	default:
		return 0, fmt.Errorf("invalid pr_action %q, must be opened/pushed/merged/commented", s)
	}
}

func parseCICompletionCondition(s string) (v1.GitCICompletionCondition, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "failure":
		return v1.GitCICompletionCondition_GIT_CI_COMPLETION_CONDITION_FAILURE, nil
	case "success":
		return v1.GitCICompletionCondition_GIT_CI_COMPLETION_CONDITION_SUCCESS, nil
	case "any":
		return v1.GitCICompletionCondition_GIT_CI_COMPLETION_CONDITION_ANY, nil
	default:
		return 0, fmt.Errorf("invalid git_ci_completed.condition %q, must be failure/success/any", s)
	}
}

func ciCompletionConditionToString(c v1.GitCICompletionCondition) string {
	switch c {
	case v1.GitCICompletionCondition_GIT_CI_COMPLETION_CONDITION_FAILURE:
		return "failure"
	case v1.GitCICompletionCondition_GIT_CI_COMPLETION_CONDITION_SUCCESS:
		return "success"
	case v1.GitCICompletionCondition_GIT_CI_COMPLETION_CONDITION_ANY:
		return "any"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Proto → Model conversion
// ---------------------------------------------------------------------------

func protoToModel(ctx context.Context, withOwner *v1.AutomationWithOwner) (platformWorkflowModel, error) {
	if withOwner == nil || withOwner.GetWorkflow() == nil {
		return platformWorkflowModel{}, fmt.Errorf("automation response is empty")
	}
	pw := withOwner.GetWorkflow()
	wf := pw.GetWorkflow()
	if wf == nil {
		return platformWorkflowModel{}, fmt.Errorf("automation definition is empty")
	}

	m := platformWorkflowModel{
		ID:          types.StringValue(pw.GetAutomationId()),
		Name:        types.StringValue(pw.GetName()),
		Description: stringOrNull(pw.GetDescription()),
		Scope:       automationScopeToModel(pw.GetScope()),
		// team_id is not echoed back: the resource keeps the configured value
		// (see preserveConfiguredValues) and the data source fills it in.
		TeamID:               types.Int64Null(),
		Enabled:              types.BoolValue(pw.GetEnabled()),
		DisabledDefaultTools: types.ListNull(types.StringType),
		CreatedAt:            types.Int64Value(pw.GetCreatedAt()),
		UpdatedAt:            types.Int64Value(pw.GetUpdatedAt()),
	}

	// DisabledDefaultTools
	var disabledTools []string
	for _, tool := range wf.GetDisabledDefaultTools() {
		if name := automationDefaultToolToString(tool); name != "" {
			disabledTools = append(disabledTools, name)
		}
	}
	if len(disabledTools) > 0 {
		m.DisabledDefaultTools, _ = types.ListValueFrom(ctx, types.StringType, disabledTools)
	}

	// Prompt (take the first one)
	if len(wf.Prompts) > 0 {
		m.Prompt = types.StringValue(wf.Prompts[0].GetPrompt())
		switch wf.Prompts[0].GetEffortLevel() {
		case v1.PromptEffortLevel_PROMPT_EFFORT_LEVEL_STANDARD:
			m.EffortLevel = types.StringValue("standard")
		case v1.PromptEffortLevel_PROMPT_EFFORT_LEVEL_HARD:
			m.EffortLevel = types.StringValue("hard")
		default:
			m.EffortLevel = types.StringNull()
		}
	} else {
		m.Prompt = types.StringValue("")
		m.EffortLevel = types.StringNull()
	}

	// Model
	if wf.Model != nil {
		m.Model = types.StringValue(wf.GetModel())
	} else {
		m.Model = types.StringNull()
	}
	m.ModelSelection = protoModelSelectionToModel(wf.GetModelSelection())

	// GitConfig
	if gc := wf.GetGitConfig(); gc != nil {
		if gc.Repo != "" {
			m.GitRepo = types.StringValue(gc.Repo)
		} else {
			m.GitRepo = types.StringNull()
		}
		if gc.Branch != "" {
			m.GitBranch = types.StringValue(gc.Branch)
		} else {
			m.GitBranch = types.StringNull()
		}
	} else {
		m.GitRepo = types.StringNull()
		m.GitBranch = types.StringNull()
	}

	// AgentOptions
	if ao := wf.GetAgentOptions(); ao != nil {
		if ao.SkipInstall != nil {
			m.SkipInstall = types.BoolValue(ao.GetSkipInstall())
		} else {
			m.SkipInstall = types.BoolNull()
		}
		if ao.EnvironmentPublicId != nil && ao.GetEnvironmentPublicId() != "" {
			m.EnvironmentPublicID = types.StringValue(ao.GetEnvironmentPublicId())
		} else {
			m.EnvironmentPublicID = types.StringNull()
		}
		if privateWorker := ao.GetPrivateWorker(); privateWorker != nil {
			labels := make(map[string]string, len(privateWorker.GetLabels()))
			for _, label := range privateWorker.GetLabels() {
				if label == nil {
					continue
				}
				if _, exists := labels[label.GetKey()]; exists {
					return platformWorkflowModel{}, fmt.Errorf("private_worker contains duplicate label key %q", label.GetKey())
				}
				labels[label.GetKey()] = label.GetValue()
			}
			labelMap, diags := types.MapValueFrom(ctx, types.StringType, labels)
			if diags.HasError() {
				return platformWorkflowModel{}, fmt.Errorf("reading private_worker.labels")
			}
			m.PrivateWorker = &privateWorkerModel{Labels: labelMap}
		} else {
			m.PrivateWorker = nil
		}
	} else {
		m.SkipInstall = types.BoolNull()
		m.EnvironmentPublicID = types.StringNull()
		m.PrivateWorker = nil
	}

	// MemoryEnabled
	if wf.MemoryEnabled != nil {
		m.MemoryEnabled = types.BoolValue(wf.GetMemoryEnabled())
	} else {
		m.MemoryEnabled = types.BoolNull()
	}

	// Triggers
	for _, t := range wf.Triggers {
		tm, err := protoTriggerToModel(ctx, t)
		if err != nil {
			return platformWorkflowModel{}, fmt.Errorf("reading trigger: %w", err)
		}
		m.Triggers = append(m.Triggers, tm)
	}

	// Actions
	for _, a := range wf.Actions {
		am := protoActionToModel(a)
		// Skip appending empty/unknown actions to avoid invalid empty action blocks.
		if am.PrComment == nil &&
			am.GitPr == nil &&
			am.RequestReviewers == nil &&
			am.Mcp == nil &&
			am.Slack == nil &&
			am.ReadSlack == nil &&
			am.MicrosoftTeams == nil &&
			am.ReadMicrosoftTeams == nil &&
			am.ManageCheckRun == nil &&
			am.ApprovePr == nil &&
			am.ResolveReviewThreads == nil {
			continue
		}
		m.Actions = append(m.Actions, am)
	}

	return m, nil
}

func protoActionToModel(a *v1.Action) actionModel {
	am := actionModel{}

	switch action := a.Action.(type) {
	case *v1.Action_PrComment:
		am.PrComment = &prCommentActionModel{
			AllowInlineComments: types.BoolValue(action.PrComment.GetAllowInlineComments()),
			AllowApprove:        types.BoolValue(action.PrComment.GetAllowApprove()),
		}
	case *v1.Action_GitPr:
		am.GitPr = &gitPrActionModel{}
	case *v1.Action_RequestReviewers:
		am.RequestReviewers = &requestReviewersActionModel{}
	case *v1.Action_Mcp:
		if server := action.Mcp.GetServer(); server != nil {
			am.Mcp = &mcpActionModel{
				Server: types.StringValue(server.GetName()),
			}
			if server.Id != nil {
				am.Mcp.ServerID = types.Int64Value(server.GetId())
			} else {
				am.Mcp.ServerID = types.Int64Null()
			}
		}
	case *v1.Action_Slack:
		slack := action.Slack
		sm := &slackActionModel{
			Generalized:     types.BoolValue(slack.GetGeneralized()),
			RespondInThread: types.BoolValue(slack.GetRespondInThread()),
			PostAsThread:    types.BoolValue(slack.GetPostAsThread()),
		}
		if slack.GetChannel() != "" {
			sm.Channel = types.StringValue(slack.GetChannel())
		} else {
			sm.Channel = types.StringNull()
		}
		am.Slack = sm
	case *v1.Action_ReadSlack:
		am.ReadSlack = &readSlackActionModel{}
	case *v1.Action_MicrosoftTeams:
		teams := action.MicrosoftTeams
		mt := &microsoftTeamsActionModel{
			Generalized:     types.BoolValue(teams.GetGeneralized()),
			RespondInThread: types.BoolValue(teams.GetRespondInThread()),
			PostAsThread:    types.BoolValue(teams.GetPostAsThread()),
			ChannelIDs:      types.ListNull(types.StringType),
		}
		if teams.GetTenantId() != "" {
			mt.TenantID = types.StringValue(teams.GetTenantId())
		} else {
			mt.TenantID = types.StringNull()
		}
		if teams.GetTeamId() != "" {
			mt.TeamID = types.StringValue(teams.GetTeamId())
		} else {
			mt.TeamID = types.StringNull()
		}
		if teams.GetChannelId() != "" {
			mt.ChannelID = types.StringValue(teams.GetChannelId())
		} else {
			mt.ChannelID = types.StringNull()
		}
		if len(teams.GetChannelIds()) > 0 {
			channelIDs, _ := types.ListValueFrom(context.Background(), types.StringType, teams.GetChannelIds())
			mt.ChannelIDs = channelIDs
		}
		am.MicrosoftTeams = mt
	case *v1.Action_ReadMicrosoftTeams:
		am.ReadMicrosoftTeams = &readMicrosoftTeamsActionModel{}
	case *v1.Action_ManageCheckRun:
		am.ManageCheckRun = &manageCheckRunActionModel{}
	case *v1.Action_ApprovePr:
		am.ApprovePr = &approvePrActionModel{}
	case *v1.Action_ResolveReviewThreads:
		am.ResolveReviewThreads = &resolveReviewThreadsActionModel{}
	}

	return am
}

func protoTriggerToModel(ctx context.Context, t *v1.Trigger) (triggerModel, error) {
	tm := triggerModel{}

	switch trigger := t.Trigger.(type) {
	case *v1.Trigger_Git:
		git := trigger.Git
		switch event := git.Event.(type) {
		case *v1.GitTrigger_PullRequest:
			pr := event.PullRequest
			prModel := &gitPullRequestModel{
				Orgs:                   types.ListNull(types.StringType),
				Repos:                  types.ListNull(types.StringType),
				IgnoreDraftPrs:         types.BoolValue(pr.GetIgnoreDraftPrs()),
				CommentContainsIsRegex: types.BoolValue(pr.GetCommentContainsIsRegex()),
			}
			if len(pr.GetOrgs()) > 0 {
				orgs, _ := types.ListValueFrom(ctx, types.StringType, pr.GetOrgs())
				prModel.Orgs = orgs
			}
			repoTargets := pr.GetRepos()
			if len(repoTargets) == 0 && pr.GetRepo() != "" {
				repoTargets = []string{pr.GetRepo()}
			}
			if len(repoTargets) > 0 {
				repos, _ := types.ListValueFrom(ctx, types.StringType, repoTargets)
				prModel.Repos = repos
			}
			if pr.GetPrAction() != v1.GitPullRequestAction_GIT_PULL_REQUEST_ACTION_UNSPECIFIED {
				prModel.PrAction = types.StringValue(prActionToString(pr.GetPrAction()))
			} else {
				prModel.PrAction = types.StringNull()
			}
			if pr.GetCommentContains() != "" {
				prModel.CommentContains = types.StringValue(pr.GetCommentContains())
			} else {
				prModel.CommentContains = types.StringNull()
			}
			tm.GitPullRequest = prModel

		case *v1.GitTrigger_Push:
			push := event.Push
			tm.GitPush = &gitPushModel{
				Repo: types.StringValue(push.GetRepo()),
			}
			if push.GetBranch() != "" {
				tm.GitPush.Branch = types.StringValue(push.GetBranch())
			} else {
				tm.GitPush.Branch = types.StringNull()
			}

		case *v1.GitTrigger_CiCompleted:
			ci := event.CiCompleted
			ciModel := &gitCICompletedModel{
				Repos:              types.ListNull(types.StringType),
				IgnoreBaseFailures: types.BoolValue(ci.GetIgnoreBaseFailures()),
			}
			if len(ci.GetRepos()) > 0 {
				repos, _ := types.ListValueFrom(ctx, types.StringType, ci.GetRepos())
				ciModel.Repos = repos
			}
			if ci.GetCondition() != v1.GitCICompletionCondition_GIT_CI_COMPLETION_CONDITION_UNSPECIFIED {
				ciModel.Condition = types.StringValue(ciCompletionConditionToString(ci.GetCondition()))
			} else {
				ciModel.Condition = types.StringNull()
			}
			if ci.GetBranch() != "" {
				ciModel.Branch = types.StringValue(ci.GetBranch())
			} else {
				ciModel.Branch = types.StringNull()
			}
			tm.GitCICompleted = ciModel

		default:
			// Unsupported git trigger sub-type; leave all nil
		}

		// User allowlist
		if len(git.GetUserAllowlist()) > 0 {
			tm.UserAllowlist, _ = types.ListValueFrom(ctx, types.StringType, git.GetUserAllowlist())
		} else {
			tm.UserAllowlist = types.ListNull(types.StringType)
		}

	case *v1.Trigger_Cron:
		tm.Cron = &cronModel{
			Schedule: types.StringValue(trigger.Cron.GetCron()),
		}
		tm.UserAllowlist = types.ListNull(types.StringType)

	case *v1.Trigger_SlackTrigger:
		slack := trigger.SlackTrigger
		sm := &slackTriggerModel{
			Channel: types.StringValue(slack.GetChannel()),
		}
		if slack.GetMessageContains() != "" {
			sm.MessageContains = types.StringValue(slack.GetMessageContains())
		} else {
			sm.MessageContains = types.StringNull()
		}
		if slack.GetMessageContainsIsRegex() {
			sm.MessageContainsIsRegex = types.BoolValue(true)
		} else {
			sm.MessageContainsIsRegex = types.BoolNull()
		}
		if slack.GetBlockUnauthenticatedSlackUsers() {
			sm.BlockUnauthenticatedSlackUsers = types.BoolValue(true)
		} else {
			sm.BlockUnauthenticatedSlackUsers = types.BoolNull()
		}
		if slack.SlackCompletionReactionMode != nil &&
			slack.GetSlackCompletionReactionMode() != v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_UNSPECIFIED {
			sm.CompletionReactionMode = types.StringValue(slackCompletionReactionModeToString(slack.GetSlackCompletionReactionMode()))
		} else {
			sm.CompletionReactionMode = types.StringNull()
		}
		if slack.GetSlackCompletionReactionCustomEmoji() != "" {
			sm.CompletionReactionCustomEmoji = types.StringValue(slack.GetSlackCompletionReactionCustomEmoji())
		} else {
			sm.CompletionReactionCustomEmoji = types.StringNull()
		}
		tm.Slack = sm
		tm.UserAllowlist = types.ListNull(types.StringType)

	case *v1.Trigger_SlackChannelCreated:
		tm.SlackChannelCreated = &slackChannelCreatedTriggerModel{
			ChannelNameContains: stringOrNull(trigger.SlackChannelCreated.GetChannelNameContains()),
		}
		tm.UserAllowlist = types.ListNull(types.StringType)

	case *v1.Trigger_SlackReactionAdded:
		sra := trigger.SlackReactionAdded
		tm.SlackReactionAdded = &slackReactionAddedTriggerModel{
			Channel:                        types.StringValue(firstSlackChannel(sra.GetChannel(), sra.GetChannels())),
			EmojiName:                      types.StringValue(sra.GetEmojiName()),
			BlockUnauthenticatedSlackUsers: boolOrNull(sra.GetBlockUnauthenticatedSlackUsers()),
			OnlyOwnerReactions:             boolOrNull(sra.GetOnlyOwnerReactions()),
		}
		tm.UserAllowlist = types.ListNull(types.StringType)

	case *v1.Trigger_SlackMention:
		sm := trigger.SlackMention
		tm.SlackMention = &slackMentionTriggerModel{
			Channel:                        types.StringValue(firstSlackChannel(sm.GetChannel(), sm.GetChannels())),
			BlockUnauthenticatedSlackUsers: boolOrNull(sm.GetBlockUnauthenticatedSlackUsers()),
		}
		tm.UserAllowlist = types.ListNull(types.StringType)

	case *v1.Trigger_SlackAnyReactionAdded:
		sar := trigger.SlackAnyReactionAdded
		tm.SlackAnyReactionAdded = &slackAnyReactionAddedTriggerModel{
			Channel:                        types.StringValue(firstSlackChannel(sar.GetChannel(), sar.GetChannels())),
			BlockUnauthenticatedSlackUsers: boolOrNull(sar.GetBlockUnauthenticatedSlackUsers()),
			OnlyOwnerReactions:             boolOrNull(sar.GetOnlyOwnerReactions()),
		}
		tm.UserAllowlist = types.ListNull(types.StringType)

	case *v1.Trigger_Pagerduty:
		pd := trigger.Pagerduty
		pm := &pagerDutyTriggerModel{ServiceIDs: types.ListNull(types.StringType)}
		if len(pd.GetServiceIds()) > 0 {
			pm.ServiceIDs, _ = types.ListValueFrom(ctx, types.StringType, pd.GetServiceIds())
		}
		switch pd.Event.(type) {
		case *v1.PagerDutyTrigger_IncidentTriggered:
			pm.IncidentTriggered = &emptyEventModel{}
		case *v1.PagerDutyTrigger_IncidentAcknowledged:
			pm.IncidentAcknowledged = &emptyEventModel{}
		case *v1.PagerDutyTrigger_IncidentResolved:
			pm.IncidentResolved = &emptyEventModel{}
		case *v1.PagerDutyTrigger_IncidentEscalated:
			pm.IncidentEscalated = &emptyEventModel{}
		case *v1.PagerDutyTrigger_IncidentAny:
			pm.IncidentAny = &emptyEventModel{}
		}
		tm.PagerDuty = pm
		tm.UserAllowlist = types.ListNull(types.StringType)

	case *v1.Trigger_Sentry:
		sentry := trigger.Sentry
		sm := &sentryTriggerModel{ProjectIDs: types.ListNull(types.StringType)}
		if len(sentry.GetProjectIds()) > 0 {
			sm.ProjectIDs, _ = types.ListValueFrom(ctx, types.StringType, sentry.GetProjectIds())
		}
		switch sentry.Event.(type) {
		case *v1.SentryTrigger_IssueCreated:
			sm.IssueCreated = &emptyEventModel{}
		case *v1.SentryTrigger_IssueResolved:
			sm.IssueResolved = &emptyEventModel{}
		case *v1.SentryTrigger_IssueAssigned:
			sm.IssueAssigned = &emptyEventModel{}
		case *v1.SentryTrigger_IssueArchived:
			sm.IssueArchived = &emptyEventModel{}
		case *v1.SentryTrigger_IssueUnresolved:
			sm.IssueUnresolved = &emptyEventModel{}
		case *v1.SentryTrigger_IssueAny:
			sm.IssueAny = &emptyEventModel{}
		}
		tm.Sentry = sm
		tm.UserAllowlist = types.ListNull(types.StringType)

	case *v1.Trigger_Linear:
		linear := trigger.Linear
		lm := &linearTriggerModel{}
		if len(linear.GetProjectIds()) > 0 {
			lm.ProjectIDs, _ = types.ListValueFrom(ctx, types.StringType, linear.GetProjectIds())
		} else {
			lm.ProjectIDs = types.ListNull(types.StringType)
		}
		if len(linear.GetTeamIds()) > 0 {
			lm.TeamIDs, _ = types.ListValueFrom(ctx, types.StringType, linear.GetTeamIds())
		} else {
			lm.TeamIDs = types.ListNull(types.StringType)
		}

		switch event := linear.Event.(type) {
		case *v1.LinearTrigger_IssueCreated:
			lm.IssueCreated = &linearIssueCreatedModel{}
		case *v1.LinearTrigger_StatusChanged:
			sm := &linearStatusChangedModel{}
			if len(event.StatusChanged.GetStatusIds()) > 0 {
				sm.StatusIDs, _ = types.ListValueFrom(ctx, types.StringType, event.StatusChanged.GetStatusIds())
			} else {
				sm.StatusIDs = types.ListNull(types.StringType)
			}
			lm.StatusChanged = sm
		case *v1.LinearTrigger_EndOfCycle:
			em := &linearEndOfCycleModel{}
			if len(event.EndOfCycle.GetCycleIds()) > 0 {
				em.CycleIDs, _ = types.ListValueFrom(ctx, types.StringType, event.EndOfCycle.GetCycleIds())
			} else {
				em.CycleIDs = types.ListNull(types.StringType)
			}
			lm.EndOfCycle = em
		}

		tm.Linear = lm
		tm.UserAllowlist = types.ListNull(types.StringType)

	case *v1.Trigger_Webhook:
		tm.Webhook = &webhookTriggerModel{}
		tm.UserAllowlist = types.ListNull(types.StringType)

	case *v1.Trigger_MicrosoftTeamsTrigger:
		teams := trigger.MicrosoftTeamsTrigger
		mt := &microsoftTeamsTriggerModel{
			TenantID:   types.StringValue(teams.GetTenantId()),
			TeamIDs:    types.ListNull(types.StringType),
			ChannelIDs: types.ListNull(types.StringType),
		}
		if teams.GetTeamId() != "" {
			mt.TeamID = types.StringValue(teams.GetTeamId())
		} else {
			mt.TeamID = types.StringNull()
		}
		if len(teams.GetTeamIds()) > 0 {
			teamIDs, _ := types.ListValueFrom(ctx, types.StringType, teams.GetTeamIds())
			mt.TeamIDs = teamIDs
		}
		if len(teams.GetChannelIds()) > 0 {
			channelIDs, _ := types.ListValueFrom(ctx, types.StringType, teams.GetChannelIds())
			mt.ChannelIDs = channelIDs
		}
		if teams.GetMessageContains() != "" {
			mt.MessageContains = types.StringValue(teams.GetMessageContains())
		} else {
			mt.MessageContains = types.StringNull()
		}
		if teams.GetMessageContainsIsRegex() {
			mt.MessageContainsIsRegex = types.BoolValue(true)
		} else {
			mt.MessageContainsIsRegex = types.BoolNull()
		}
		if teams.GetBlockUnauthenticatedTeamsUsers() {
			mt.BlockUnauthenticatedTeamsUsers = types.BoolValue(true)
		} else {
			mt.BlockUnauthenticatedTeamsUsers = types.BoolNull()
		}
		tm.MicrosoftTeams = mt
		tm.UserAllowlist = types.ListNull(types.StringType)

	case *v1.Trigger_MicrosoftTeamsChannelCreated:
		mtc := trigger.MicrosoftTeamsChannelCreated
		mctt := &microsoftTeamsChannelCreatedTriggerModel{
			TenantID: types.StringValue(mtc.GetTenantId()),
			TeamIDs:  types.ListNull(types.StringType),
		}
		if len(mtc.GetTeamIds()) > 0 {
			teamIDs, _ := types.ListValueFrom(ctx, types.StringType, mtc.GetTeamIds())
			mctt.TeamIDs = teamIDs
		}
		if mtc.GetChannelNameContains() != "" {
			mctt.ChannelNameContains = types.StringValue(mtc.GetChannelNameContains())
		} else {
			mctt.ChannelNameContains = types.StringNull()
		}
		tm.MicrosoftTeamsChannelCreated = mctt
		tm.UserAllowlist = types.ListNull(types.StringType)

	default:
		tm.UserAllowlist = types.ListNull(types.StringType)
	}

	return tm, nil
}

// firstSlackChannel mirrors the server's preferRepeated: the repeated
// `channels` field wins over the singular `channel` when populated. Only the
// first channel is exposed, matching the existing `slack` trigger.
func firstSlackChannel(channel string, channels []string) string {
	for _, c := range channels {
		if strings.TrimSpace(c) != "" {
			return strings.TrimSpace(c)
		}
	}
	return channel
}

func prActionToString(a v1.GitPullRequestAction) string {
	switch a {
	case v1.GitPullRequestAction_GIT_PULL_REQUEST_ACTION_OPENED:
		return "opened"
	case v1.GitPullRequestAction_GIT_PULL_REQUEST_ACTION_PUSHED:
		return "pushed"
	case v1.GitPullRequestAction_GIT_PULL_REQUEST_ACTION_MERGED:
		return "merged"
	case v1.GitPullRequestAction_GIT_PULL_REQUEST_ACTION_COMMENTED:
		return "commented"
	default:
		return ""
	}
}
