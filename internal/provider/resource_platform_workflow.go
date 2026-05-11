package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	connect "connectrpc.com/connect"
	v1 "github.com/cursor/terraform-provider-cursor/internal/proto/v1"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
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
	ID            types.String   `tfsdk:"id"`
	Name          types.String   `tfsdk:"name"`
	Scope         types.String   `tfsdk:"scope"`
	Enabled       types.Bool     `tfsdk:"enabled"`
	Prompt        types.String   `tfsdk:"prompt"`
	EffortLevel   types.String   `tfsdk:"effort_level"`
	Model         types.String   `tfsdk:"model"`
	GitRepo       types.String   `tfsdk:"git_repo"`
	GitBranch     types.String   `tfsdk:"git_branch"`
	SkipInstall   types.Bool     `tfsdk:"skip_install"`
	MemoryEnabled types.Bool     `tfsdk:"memory_enabled"`
	Triggers      []triggerModel `tfsdk:"trigger"`
	Actions       []actionModel  `tfsdk:"action"`
	CreatedAt     types.Int64    `tfsdk:"created_at"`
	UpdatedAt     types.Int64    `tfsdk:"updated_at"`
}

type triggerModel struct {
	GitPullRequest               *gitPullRequestModel                      `tfsdk:"git_pull_request"`
	GitPush                      *gitPushModel                             `tfsdk:"git_push"`
	Cron                         *cronModel                                `tfsdk:"cron"`
	Slack                        *slackTriggerModel                        `tfsdk:"slack"`
	Linear                       *linearTriggerModel                       `tfsdk:"linear"`
	Webhook                      *webhookTriggerModel                      `tfsdk:"webhook"`
	MicrosoftTeams               *microsoftTeamsTriggerModel               `tfsdk:"microsoft_teams"`
	MicrosoftTeamsChannelCreated *microsoftTeamsChannelCreatedTriggerModel `tfsdk:"microsoft_teams_channel_created"`
	UserAllowlist                types.List                                `tfsdk:"user_allowlist"`
}

type gitPullRequestModel struct {
	Orgs            types.List   `tfsdk:"orgs"`
	Repos           types.List   `tfsdk:"repos"`
	IgnoreDraftPrs  types.Bool   `tfsdk:"ignore_draft_prs"`
	PrAction        types.String `tfsdk:"pr_action"`
	CommentContains types.String `tfsdk:"comment_contains"`
}

type gitPushModel struct {
	Repo   types.String `tfsdk:"repo"`
	Branch types.String `tfsdk:"branch"`
}

type cronModel struct {
	Schedule types.String `tfsdk:"schedule"`
}

type slackTriggerModel struct {
	Channel                        types.String `tfsdk:"channel"`
	MessageContains                types.String `tfsdk:"message_contains"`
	MessageContainsIsRegex         types.Bool   `tfsdk:"message_contains_is_regex"`
	BlockUnauthenticatedSlackUsers types.Bool   `tfsdk:"block_unauthenticated_slack_users"`
}

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
	PrComment          *prCommentActionModel          `tfsdk:"pr_comment"`
	GitPr              *gitPrActionModel              `tfsdk:"git_pr"`
	RequestReviewers   *requestReviewersActionModel   `tfsdk:"request_reviewers"`
	Mcp                *mcpActionModel                `tfsdk:"mcp"`
	Slack              *slackActionModel              `tfsdk:"slack"`
	ReadSlack          *readSlackActionModel          `tfsdk:"read_slack"`
	MicrosoftTeams     *microsoftTeamsActionModel     `tfsdk:"microsoft_teams"`
	ReadMicrosoftTeams *readMicrosoftTeamsActionModel `tfsdk:"read_microsoft_teams"`
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
	Server types.String `tfsdk:"server"`
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

func NewPlatformWorkflowResource() resource.Resource {
	return &platformWorkflowResource{}
}

func (r *platformWorkflowResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_platform_workflow"
}

func (r *platformWorkflowResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
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
				Description: "Model to use (e.g. claude-4.6-opus-high-thinking, gpt-4o).",
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
			"memory_enabled": schema.BoolAttribute{
				Optional:    true,
				Description: "Enable the AutomationMemory tool, giving the agent persistent memory across runs.",
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
									Optional:    true,
									ElementType: types.StringType,
									Description: "Optional AAD group IDs to scope to. Empty fires for any team in the tenant.",
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
									Optional:    true,
									Computed:    true,
									Description: "If true, respond in the thread of the triggering Slack message (Slack triggers only).",
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

	createResp, err := r.client.automations.CreateAutomation(
		ctx,
		connect.NewRequest(&v1.CreateAutomationRequest{
			Name:     plan.Name.ValueString(),
			Scope:    scope,
			Workflow: workflow,
		}),
	)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create automation", connectErrorMessage(err))
		return
	}

	state, err := protoToModel(ctx, createResp.Msg.GetWorkflow())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read automation", err.Error())
		return
	}
	preserveEquivalentGitPullRequestOrgs(ctx, &state, plan)

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
		preserveEquivalentGitPullRequestOrgs(ctx, &updated, plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &updated)...)
	}
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
		connect.NewRequest(&v1.GetAutomationRequest{AutomationId: workflowID}),
	)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read automation", connectErrorMessage(err))
		return
	}

	updatedState, err := protoToModel(ctx, workflowResp.Msg.GetWorkflow())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read automation", err.Error())
		return
	}
	preserveEquivalentGitPullRequestOrgs(ctx, &updatedState, state)

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
	preserveEquivalentGitPullRequestOrgs(ctx, &updatedState, plan)

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

	// Model
	if !m.Model.IsNull() && !m.Model.IsUnknown() {
		model := m.Model.ValueString()
		w.Model = &model
	}

	// GitConfig (for non-git triggers)
	if (!m.GitRepo.IsNull() && !m.GitRepo.IsUnknown()) || (!m.GitBranch.IsNull() && !m.GitBranch.IsUnknown()) {
		gc := &v1.GitConfig{}
		if !m.GitRepo.IsNull() && !m.GitRepo.IsUnknown() {
			gc.Repo = m.GitRepo.ValueString()
		}
		if !m.GitBranch.IsNull() && !m.GitBranch.IsUnknown() {
			gc.Branch = m.GitBranch.ValueString()
		}
		w.GitConfig = gc
	}

	// AgentOptions
	if !m.SkipInstall.IsNull() && !m.SkipInstall.IsUnknown() {
		skip := m.SkipInstall.ValueBool()
		w.AgentOptions = &v1.AgentOptions{SkipInstall: &skip}
	}

	// MemoryEnabled
	if !m.MemoryEnabled.IsNull() && !m.MemoryEnabled.IsUnknown() {
		enabled := m.MemoryEnabled.ValueBool()
		w.MemoryEnabled = &enabled
	}

	// Triggers
	for i, t := range m.Triggers {
		trigger, err := triggerModelToProto(ctx, &t)
		if err != nil {
			return nil, fmt.Errorf("trigger[%d]: %w", i, err)
		}
		w.Triggers = append(w.Triggers, trigger)
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
	if count == 0 {
		return nil, fmt.Errorf("must specify exactly one of pr_comment, git_pr, request_reviewers, mcp, slack, read_slack, microsoft_teams, or read_microsoft_teams")
	}
	if count > 1 {
		return nil, fmt.Errorf("must specify exactly one of pr_comment, git_pr, request_reviewers, mcp, slack, read_slack, microsoft_teams, or read_microsoft_teams")
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
		return &v1.Action{
			Action: &v1.Action_Mcp{
				Mcp: &v1.McpAction{
					Server: &v1.McpServerConfig{Name: a.Mcp.Server.ValueString()},
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
		return &v1.Action{Action: &v1.Action_MicrosoftTeams{MicrosoftTeams: teams}}, nil
	}
	if a.ReadMicrosoftTeams != nil {
		return &v1.Action{Action: &v1.Action_ReadMicrosoftTeams{ReadMicrosoftTeams: &v1.ReadMicrosoftTeamsAction{}}}, nil
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
	if count == 0 {
		return nil, fmt.Errorf("must specify exactly one of git_pull_request, git_push, cron, slack, linear, webhook, microsoft_teams, or microsoft_teams_channel_created")
	}
	if count > 1 {
		return nil, fmt.Errorf("must specify exactly one of git_pull_request, git_push, cron, slack, linear, webhook, microsoft_teams, or microsoft_teams_channel_created")
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
		trigger.Trigger = &v1.Trigger_SlackTrigger{SlackTrigger: st}
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
		if !mtc.TeamIDs.IsNull() && !mtc.TeamIDs.IsUnknown() {
			var teamIDs []string
			diags := mtc.TeamIDs.ElementsAs(ctx, &teamIDs, false)
			if diags.HasError() {
				return nil, fmt.Errorf("failed to read microsoft_teams_channel_created.team_ids")
			}
			mctt.TeamIds = teamIDs
		}
		if !mtc.ChannelNameContains.IsNull() && !mtc.ChannelNameContains.IsUnknown() {
			mctt.ChannelNameContains = mtc.ChannelNameContains.ValueString()
		}
		trigger.Trigger = &v1.Trigger_MicrosoftTeamsChannelCreated{MicrosoftTeamsChannelCreated: mctt}
	}

	return trigger, nil
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
		ID:        types.StringValue(pw.GetAutomationId()),
		Name:      types.StringValue(pw.GetName()),
		Scope:     automationScopeToModel(pw.GetScope()),
		Enabled:   types.BoolValue(pw.GetEnabled()),
		CreatedAt: types.Int64Value(pw.GetCreatedAt()),
		UpdatedAt: types.Int64Value(pw.GetUpdatedAt()),
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
	if ao := wf.GetAgentOptions(); ao != nil && ao.SkipInstall != nil {
		m.SkipInstall = types.BoolValue(ao.GetSkipInstall())
	} else {
		m.SkipInstall = types.BoolNull()
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
			am.ReadMicrosoftTeams == nil {
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
		if action.Mcp.GetServer() != nil {
			am.Mcp = &mcpActionModel{
				Server: types.StringValue(action.Mcp.GetServer().GetName()),
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
				Orgs:           types.ListNull(types.StringType),
				Repos:          types.ListNull(types.StringType),
				IgnoreDraftPrs: types.BoolValue(pr.GetIgnoreDraftPrs()),
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
		tm.Slack = sm
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
