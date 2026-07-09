package provider

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"
	v1 "github.com/cursor/terraform-provider-cursor/internal/proto/v1"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type platformWorkflowDataSource struct {
	client *apiClient
}

func NewPlatformWorkflowDataSource() datasource.DataSource {
	return &platformWorkflowDataSource{}
}

func (d *platformWorkflowDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_platform_workflow"
}

func (d *platformWorkflowDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing Cursor Automation by ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Required:    true,
				Description: "Automation ID.",
			},
			"name": schema.StringAttribute{
				Computed:    true,
				Description: "Display name for the automation.",
			},
			"scope": schema.StringAttribute{
				Computed:    true,
				Description: `Automation ownership scope: "user" or "team".`,
			},
			"enabled": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the automation is enabled.",
			},
			"prompt": schema.StringAttribute{
				Computed:    true,
				Description: "The prompt text.",
			},
			"effort_level": schema.StringAttribute{
				Computed:    true,
				Description: "Effort level: standard or hard.",
			},
			"model": schema.StringAttribute{
				Computed:    true,
				Description: "Model name.",
			},
			"git_repo": schema.StringAttribute{
				Computed:    true,
				Description: "Git repository for non-git triggers.",
			},
			"git_branch": schema.StringAttribute{
				Computed:    true,
				Description: "Git branch for non-git triggers.",
			},
			"skip_install": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether to skip install commands.",
			},
			"memory_enabled": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the AutomationMemory tool is enabled for persistent memory across runs.",
			},
			"trigger": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Triggers that start the automation.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"git_pull_request": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Trigger on GitHub pull request events.",
							Attributes: map[string]schema.Attribute{
								"orgs": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
									Description: "GitHub orgs to watch.",
								},
								"repos": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
									Description: "GitHub repos to watch.",
								},
								"ignore_draft_prs": schema.BoolAttribute{
									Computed:    true,
									Description: "Do not trigger on draft PRs.",
								},
								"pr_action": schema.StringAttribute{
									Computed:    true,
									Description: "PR action that triggers the automation.",
								},
								"comment_contains": schema.StringAttribute{
									Computed:    true,
									Description: "Comment text filter for the commented PR action.",
								},
								"comment_contains_is_regex": schema.BoolAttribute{
									Computed:    true,
									Description: "Whether comment_contains is a regex pattern.",
								},
							},
						},
						"git_push": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Trigger on git push events.",
							Attributes: map[string]schema.Attribute{
								"repo": schema.StringAttribute{
									Computed:    true,
									Description: "Repository to watch.",
								},
								"branch": schema.StringAttribute{
									Computed:    true,
									Description: "Branch to watch.",
								},
							},
						},
						"git_ci_completed": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Trigger when all CI checks complete on a PR or a specific branch.",
							Attributes: map[string]schema.Attribute{
								"repos": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
									Description: "GitHub repos to watch.",
								},
								"condition": schema.StringAttribute{
									Computed:    true,
									Description: `CI outcome that fires the trigger: "failure", "success", or "any".`,
								},
								"ignore_base_failures": schema.BoolAttribute{
									Computed:    true,
									Description: "Whether CI failures that also exist on the base branch are ignored.",
								},
								"branch": schema.StringAttribute{
									Computed:    true,
									Description: "Branch watched for CI completion instead of PRs, when set.",
								},
							},
						},
						"cron": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Trigger on a cron schedule.",
							Attributes: map[string]schema.Attribute{
								"schedule": schema.StringAttribute{
									Computed:    true,
									Description: "Cron expression.",
								},
							},
						},
						"slack": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Trigger on Slack messages.",
							Attributes: map[string]schema.Attribute{
								"channel": schema.StringAttribute{
									Computed:    true,
									Description: "Slack channel ID.",
								},
								"message_contains": schema.StringAttribute{
									Computed:    true,
									Description: "Message text filter (case-insensitive).",
								},
								"message_contains_is_regex": schema.BoolAttribute{
									Computed:    true,
									Description: "Whether message_contains is a regex pattern.",
								},
								"block_unauthenticated_slack_users": schema.BoolAttribute{
									Computed:    true,
									Description: "Whether only Slack users who linked Cursor can trigger.",
								},
								"completion_reaction_mode": schema.StringAttribute{
									Computed:    true,
									Description: `Emoji reaction behavior on successful completion: "on", "off", or "custom".`,
								},
								"completion_reaction_custom_emoji": schema.StringAttribute{
									Computed:    true,
									Description: `Custom Slack reaction emoji in ":emoji_name:" form, used when completion_reaction_mode is "custom".`,
								},
							},
						},
						"linear": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Trigger on Linear events.",
							Attributes: map[string]schema.Attribute{
								"issue_created": schema.SingleNestedAttribute{
									Computed:    true,
									Description: "Trigger when a Linear issue is created.",
									Attributes:  map[string]schema.Attribute{},
								},
								"status_changed": schema.SingleNestedAttribute{
									Computed:    true,
									Description: "Trigger when a Linear issue status changes.",
									Attributes: map[string]schema.Attribute{
										"status_ids": schema.ListAttribute{
											Computed:    true,
											ElementType: types.StringType,
											Description: "Linear status IDs to match.",
										},
									},
								},
								"end_of_cycle": schema.SingleNestedAttribute{
									Computed:    true,
									Description: "Trigger at the end of a Linear cycle.",
									Attributes: map[string]schema.Attribute{
										"cycle_ids": schema.ListAttribute{
											Computed:    true,
											ElementType: types.StringType,
											Description: "Linear cycle IDs to match.",
										},
									},
								},
								"project_ids": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
									Description: "Linear project IDs scoping issue events.",
								},
								"team_ids": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
									Description: "Linear team IDs scoping cycle events.",
								},
							},
						},
						"webhook": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Trigger on generic webhook POST requests.",
							Attributes:  map[string]schema.Attribute{},
						},
						"microsoft_teams": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Trigger on Microsoft Teams channel messages.",
							Attributes: map[string]schema.Attribute{
								"tenant_id": schema.StringAttribute{
									Computed:    true,
									Description: "AAD tenant GUID hosting the team.",
								},
								"team_id": schema.StringAttribute{
									Computed:    true,
									Description: "AAD group ID for a single configured team.",
								},
								"team_ids": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
									Description: "AAD group IDs for multiple teams.",
								},
								"channel_ids": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
									Description: "Microsoft Teams channel IDs. Empty fires for any channel in the configured team(s).",
								},
								"message_contains": schema.StringAttribute{
									Computed:    true,
									Description: "Message text filter (case-insensitive).",
								},
								"message_contains_is_regex": schema.BoolAttribute{
									Computed:    true,
									Description: "Whether message_contains is a regex pattern.",
								},
								"block_unauthenticated_teams_users": schema.BoolAttribute{
									Computed:    true,
									Description: "Whether only Microsoft Teams users who linked Cursor can trigger.",
								},
							},
						},
						"microsoft_teams_channel_created": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Trigger when a new Microsoft Teams channel is created.",
							Attributes: map[string]schema.Attribute{
								"tenant_id": schema.StringAttribute{
									Computed:    true,
									Description: "AAD tenant GUID hosting the team.",
								},
								"team_ids": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
									Description: "AAD group IDs to scope to. Empty fires for any team in the tenant.",
								},
								"channel_name_contains": schema.StringAttribute{
									Computed:    true,
									Description: "Channel name filter (case-insensitive).",
								},
							},
						},
						"user_allowlist": schema.ListAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: "Git usernames allowed to trigger this automation. Empty means all users.",
						},
					},
				},
			},
			"action": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Actions the automation can perform.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"pr_comment": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Post a comment on the PR.",
							Attributes: map[string]schema.Attribute{
								"allow_inline_comments": schema.BoolAttribute{
									Computed:    true,
									Description: "If true, the agent can post a PR review with inline comments on specific diff lines; if false or unset, only a single top-level comment is posted.",
								},
								"allow_approve": schema.BoolAttribute{
									Computed:    true,
									Description: "If true, the agent can approve or dismiss approvals on the PR using the PR comment tool.",
								},
							},
						},
						"git_pr": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Create a pull request.",
							Attributes:  map[string]schema.Attribute{},
						},
						"request_reviewers": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Request reviewers on the PR.",
							Attributes:  map[string]schema.Attribute{},
						},
						"mcp": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Enable an MCP server for this automation.",
							Attributes: map[string]schema.Attribute{
								"server": schema.StringAttribute{
									Computed:    true,
									Description: "MCP server name.",
								},
								"server_id": schema.Int64Attribute{
									Computed:    true,
									Description: "Stable MCP server ID.",
								},
							},
						},
						"slack": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Post messages to a Slack channel.",
							Attributes: map[string]schema.Attribute{
								"channel": schema.StringAttribute{
									Computed:    true,
									Description: "Slack channel ID to post to.",
								},
								"generalized": schema.BoolAttribute{
									Computed:    true,
									Description: "If true, agent can list and send to any Slack channel or DM dynamically.",
								},
								"respond_in_thread": schema.BoolAttribute{
									Computed:    true,
									Description: "If true, respond in the thread of the triggering Slack message (Slack triggers only).",
								},
								"post_as_thread": schema.BoolAttribute{
									Computed:    true,
									Description: "If true, post a parent message with the automation name and reply in the thread.",
								},
							},
						},
						"read_slack": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Give the agent read-only access to public Slack channels (ListSlackChannels, ReadSlackMessages tools).",
							Attributes:  map[string]schema.Attribute{},
						},
						"microsoft_teams": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Post messages to a Microsoft Teams channel.",
							Attributes: map[string]schema.Attribute{
								"tenant_id": schema.StringAttribute{
									Computed:    true,
									Description: "AAD tenant GUID for the destination team.",
								},
								"team_id": schema.StringAttribute{
									Computed:    true,
									Description: "AAD group ID of the destination team.",
								},
								"channel_id": schema.StringAttribute{
									Computed:    true,
									Description: "Microsoft Teams channel ID to post to.",
								},
								"channel_ids": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
									Description: "Multiple Teams channel IDs. Takes precedence over channel_id.",
								},
								"generalized": schema.BoolAttribute{
									Computed:    true,
									Description: "If true, the agent can list and post to any team/channel dynamically.",
								},
								"respond_in_thread": schema.BoolAttribute{
									Computed:    true,
									Description: "If true, respond in the thread of the triggering Microsoft Teams message.",
								},
								"post_as_thread": schema.BoolAttribute{
									Computed:    true,
									Description: "If true, post a parent message with the automation name and reply in the thread.",
								},
							},
						},
						"read_microsoft_teams": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Give the agent read-only access to Microsoft Teams channels (ListMicrosoftTeamsChannels, ReadMicrosoftTeamsMessages tools).",
							Attributes:  map[string]schema.Attribute{},
						},
					},
				},
			},
			"created_at": schema.Int64Attribute{
				Computed:    true,
				Description: "Unix timestamp (seconds) when the automation was created.",
			},
			"updated_at": schema.Int64Attribute{
				Computed:    true,
				Description: "Unix timestamp (seconds) when the automation was last updated.",
			},
		},
	}
}

func (d *platformWorkflowDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*apiClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *apiClient, got %T", req.ProviderData))
		return
	}
	d.client = client
}

func (d *platformWorkflowDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config platformWorkflowModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Async platform client is unavailable.")
		return
	}

	workflowID, err := parseWorkflowID(config.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid automation ID", err.Error())
		return
	}

	workflowResp, err := d.client.automations.GetAutomation(
		ctx,
		connect.NewRequest(&v1.GetAutomationRequest{AutomationId: workflowID}),
	)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read automation", connectErrorMessage(err))
		return
	}

	state, err := protoToModel(ctx, workflowResp.Msg.GetWorkflow())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read automation", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
