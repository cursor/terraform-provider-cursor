package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/cursor/terraform-provider-cursor/internal/proto/v1"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func mustStringList(t *testing.T, ctx context.Context, values []string) types.List {
	t.Helper()

	list, diags := types.ListValueFrom(ctx, types.StringType, values)
	if diags.HasError() {
		t.Fatalf("failed to build string list: %v", diags)
	}
	return list
}

// TestUpdatedAtHasNoPlanModifiers verifies that the updated_at attribute does
// NOT use UseStateForUnknown (or any other plan modifier), because its value
// changes server-side on every update. Using UseStateForUnknown would cause
// Terraform to plan the old timestamp and then reject the new one returned by
// the server ("Provider produced inconsistent result after apply").
func TestUpdatedAtHasNoPlanModifiers(t *testing.T) {
	r := &platformWorkflowResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, schemaResp)

	attr, ok := schemaResp.Schema.Attributes["updated_at"]
	if !ok {
		t.Fatal("schema is missing updated_at attribute")
	}

	int64Attr, ok := attr.(schema.Int64Attribute)
	if !ok {
		t.Fatalf("updated_at is not an Int64Attribute, got %T", attr)
	}

	if !int64Attr.Computed {
		t.Error("updated_at should be Computed")
	}

	if len(int64Attr.PlanModifiers) != 0 {
		t.Errorf("updated_at should have no PlanModifiers (server updates it on every write), got %d", len(int64Attr.PlanModifiers))
	}
}

// TestCreatedAtUsesUseStateForUnknown verifies that created_at, which is
// immutable after creation, keeps its UseStateForUnknown plan modifier.
func TestCreatedAtUsesUseStateForUnknown(t *testing.T) {
	r := &platformWorkflowResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, schemaResp)

	attr, ok := schemaResp.Schema.Attributes["created_at"]
	if !ok {
		t.Fatal("schema is missing created_at attribute")
	}

	int64Attr, ok := attr.(schema.Int64Attribute)
	if !ok {
		t.Fatalf("created_at is not an Int64Attribute, got %T", attr)
	}

	if !int64Attr.Computed {
		t.Error("created_at should be Computed")
	}

	if len(int64Attr.PlanModifiers) == 0 {
		t.Error("created_at should have at least one PlanModifier (UseStateForUnknown)")
	}
}

// TestWorkflowLevelActionsRoundTrip verifies that workflow-level actions
// round-trip correctly through model→proto and proto→model conversions.
func TestWorkflowLevelActionsRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		repos, _ := types.ListValueFrom(ctx, types.StringType, []string{"org/repo"})
		m := &platformWorkflowModel{
			Prompt: types.StringValue("review code"),
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Repos:          repos,
						IgnoreDraftPrs: types.BoolNull(),
						PrAction:       types.StringNull(),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
			Actions: []actionModel{
				{
					PrComment: &prCommentActionModel{
						AllowInlineComments: types.BoolValue(true),
						AllowApprove:        types.BoolValue(true),
					},
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}

		if len(wf.Actions) != 1 {
			t.Fatalf("expected 1 workflow action, got %d", len(wf.Actions))
		}
		prComment := wf.Actions[0].GetPrComment()
		if prComment == nil {
			t.Fatal("expected PrCommentAction, got nil")
		}
		if !prComment.AllowInlineComments {
			t.Error("expected AllowInlineComments=true")
		}
		if !prComment.AllowApprove {
			t.Error("expected AllowApprove=true")
		}
	})

	t.Run("proto_to_model", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				AutomationId: "test-id",
				Name:         "test-workflow",
				Enabled:      true,
				CreatedAt:    1700000000,
				UpdatedAt:    1700000000,
				Workflow: &v1.Workflow{
					Prompts: []*v1.Prompt{{Prompt: "do something"}},
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_Cron{
								Cron: &v1.CronTrigger{Cron: "0 9 * * *"},
							},
						},
					},
					Actions: []*v1.Action{
						{Action: &v1.Action_PrComment{
							PrComment: &v1.PrCommentAction{
								AllowInlineComments: true,
								AllowApprove:        true,
							},
						}},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}

		if len(model.Actions) != 1 {
			t.Fatalf("expected 1 action, got %d", len(model.Actions))
		}
		if model.Actions[0].PrComment == nil {
			t.Fatal("expected pr_comment action, got nil")
		}
		if !model.Actions[0].PrComment.AllowInlineComments.ValueBool() {
			t.Error("expected allow_inline_comments=true")
		}
		if !model.Actions[0].PrComment.AllowApprove.ValueBool() {
			t.Error("expected allow_approve=true")
		}
	})

	t.Run("proto_to_model_pr_comment_allow_inline_comments_false", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Actions: []*v1.Action{
						{Action: &v1.Action_PrComment{
							PrComment: &v1.PrCommentAction{
								AllowInlineComments: false,
							},
						}},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}

		if model.Actions[0].PrComment == nil {
			t.Fatal("expected pr_comment action, got nil")
		}
		if model.Actions[0].PrComment.AllowInlineComments.ValueBool() {
			t.Error("expected allow_inline_comments=false")
		}
	})
}

func TestWebhookTriggerRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("handle webhook"),
			Triggers: []triggerModel{
				{
					Webhook:       &webhookTriggerModel{},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		if len(wf.Triggers) != 1 {
			t.Fatalf("expected 1 trigger, got %d", len(wf.Triggers))
		}
		if wf.Triggers[0].GetWebhook() == nil {
			t.Fatal("expected webhook trigger in proto")
		}
	})

	t.Run("proto_to_model", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				AutomationId: "test-id",
				Name:         "test-workflow",
				Enabled:      true,
				Workflow: &v1.Workflow{
					Prompts: []*v1.Prompt{{Prompt: "handle webhook"}},
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_Webhook{
								Webhook: &v1.WebhookTrigger{},
							},
						},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		if len(model.Triggers) != 1 {
			t.Fatalf("expected 1 trigger, got %d", len(model.Triggers))
		}
		if model.Triggers[0].Webhook == nil {
			t.Fatal("expected webhook trigger in terraform model")
		}
	})
}

func TestGitPullRequestTargetsRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("review code"),
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs:            mustStringList(t, ctx, []string{"Example-Org"}),
						Repos:           types.ListNull(types.StringType),
						IgnoreDraftPrs:  types.BoolValue(true),
						PrAction:        types.StringNull(),
						CommentContains: types.StringNull(),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}

		pr := wf.Triggers[0].GetGit().GetPullRequest()
		if pr == nil {
			t.Fatal("expected pull request trigger in proto")
		}
		if got, want := pr.GetOrgs(), []string{"example-org"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("orgs = %v, want %v", got, want)
		}
		if got, want := pr.GetRepos(), []string{}; !reflect.DeepEqual(got, want) {
			t.Fatalf("repos = %v, want %v", got, want)
		}
	})

	t.Run("proto_to_model_preserves_legacy_mixed_targets", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				AutomationId: "test-id",
				Name:         "test-workflow",
				Enabled:      true,
				Workflow: &v1.Workflow{
					Prompts: []*v1.Prompt{{Prompt: "review code"}},
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_Git{
								Git: &v1.GitTrigger{
									Event: &v1.GitTrigger_PullRequest{
										PullRequest: &v1.GitPullRequestEvent{
											Orgs:  []string{"example-org"},
											Repos: []string{"example-org/example-repo"},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}

		if len(model.Triggers) != 1 || model.Triggers[0].GitPullRequest == nil {
			t.Fatalf("expected one git_pull_request trigger, got %+v", model.Triggers)
		}

		var orgs []string
		diags := model.Triggers[0].GitPullRequest.Orgs.ElementsAs(ctx, &orgs, false)
		if diags.HasError() {
			t.Fatalf("failed to read orgs from model: %v", diags)
		}
		if got, want := orgs, []string{"example-org"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("orgs = %v, want %v", got, want)
		}

		var repos []string
		diags = model.Triggers[0].GitPullRequest.Repos.ElementsAs(ctx, &repos, false)
		if diags.HasError() {
			t.Fatalf("failed to read repos from model: %v", diags)
		}
		if got, want := repos, []string{"example-org/example-repo"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("repos = %v, want %v", got, want)
		}
	})

	t.Run("proto_to_model_org_only_keeps_repos_null", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_Git{
								Git: &v1.GitTrigger{
									Event: &v1.GitTrigger_PullRequest{
										PullRequest: &v1.GitPullRequestEvent{
											Orgs: []string{"example-org"},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}

		if model.Triggers[0].GitPullRequest == nil {
			t.Fatal("expected git_pull_request trigger in model")
		}
		if !model.Triggers[0].GitPullRequest.Repos.IsNull() {
			t.Fatalf("expected repos to remain null for org-only trigger, got %#v", model.Triggers[0].GitPullRequest.Repos)
		}
	})

	t.Run("proto_to_model_falls_back_to_singular_repo", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_Git{
								Git: &v1.GitTrigger{
									Event: &v1.GitTrigger_PullRequest{
										PullRequest: &v1.GitPullRequestEvent{
											Repo: "owner/repo",
										},
									},
								},
							},
						},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}

		var repos []string
		diags := model.Triggers[0].GitPullRequest.Repos.ElementsAs(ctx, &repos, false)
		if diags.HasError() {
			t.Fatalf("failed to read repos from model: %v", diags)
		}
		if got, want := repos, []string{"owner/repo"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("repos = %v, want %v", got, want)
		}
	})
}

func TestPreserveEquivalentGitPullRequestOrgs(t *testing.T) {
	ctx := context.Background()

	t.Run("preserves_reference_casing_for_equivalent_orgs", func(t *testing.T) {
		state := &platformWorkflowModel{
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs: mustStringList(t, ctx, []string{"example-org", "example-inc"}),
					},
				},
			},
		}
		reference := platformWorkflowModel{
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs: mustStringList(t, ctx, []string{"Example-Org", "Example-Inc"}),
					},
				},
			},
		}

		preserveEquivalentGitPullRequestOrgs(ctx, state, reference)

		var got []string
		diags := state.Triggers[0].GitPullRequest.Orgs.ElementsAs(ctx, &got, false)
		if diags.HasError() {
			t.Fatalf("failed to read orgs from state: %v", diags)
		}
		if want := []string{"Example-Org", "Example-Inc"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("orgs = %v, want %v", got, want)
		}
	})

	t.Run("leaves_state_unchanged_for_non_equivalent_orgs", func(t *testing.T) {
		state := &platformWorkflowModel{
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs: mustStringList(t, ctx, []string{"example-org"}),
					},
				},
			},
		}
		reference := platformWorkflowModel{
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs: mustStringList(t, ctx, []string{"othersphere"}),
					},
				},
			},
		}

		preserveEquivalentGitPullRequestOrgs(ctx, state, reference)

		var got []string
		diags := state.Triggers[0].GitPullRequest.Orgs.ElementsAs(ctx, &got, false)
		if diags.HasError() {
			t.Fatalf("failed to read orgs from state: %v", diags)
		}
		if want := []string{"example-org"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("orgs = %v, want %v", got, want)
		}
	})
}

func TestModelToWorkflowRejectsMixedGitPullRequestTargets(t *testing.T) {
	ctx := context.Background()
	m := &platformWorkflowModel{
		Prompt: types.StringValue("review code"),
		Triggers: []triggerModel{
			{
				GitPullRequest: &gitPullRequestModel{
					Orgs:            mustStringList(t, ctx, []string{"example-org"}),
					Repos:           mustStringList(t, ctx, []string{"example-org/service-one"}),
					IgnoreDraftPrs:  types.BoolNull(),
					PrAction:        types.StringNull(),
					CommentContains: types.StringNull(),
				},
				UserAllowlist: types.ListNull(types.StringType),
			},
		},
	}

	_, err := modelToWorkflow(ctx, m)
	if err == nil {
		t.Fatal("expected modelToWorkflow() to reject mixed org/repo targets")
	}
	if !strings.Contains(err.Error(), "git_pull_request cannot specify both repos and orgs") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestModelToWorkflowRejectsEmptyGitPullRequestTargets(t *testing.T) {
	ctx := context.Background()
	m := &platformWorkflowModel{
		Prompt: types.StringValue("review code"),
		Triggers: []triggerModel{
			{
				GitPullRequest: &gitPullRequestModel{
					Orgs:            types.ListNull(types.StringType),
					Repos:           types.ListNull(types.StringType),
					IgnoreDraftPrs:  types.BoolNull(),
					PrAction:        types.StringNull(),
					CommentContains: types.StringNull(),
				},
				UserAllowlist: types.ListNull(types.StringType),
			},
		},
	}

	_, err := modelToWorkflow(ctx, m)
	if err == nil {
		t.Fatal("expected modelToWorkflow() to reject empty org/repo targets")
	}
	if !strings.Contains(err.Error(), "git_pull_request must specify at least one of orgs or repos") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestModelToWorkflowAllowsGitLabRepoOnly(t *testing.T) {
	ctx := context.Background()
	m := &platformWorkflowModel{
		Prompt: types.StringValue("review code"),
		Triggers: []triggerModel{
			{
				GitPullRequest: &gitPullRequestModel{
					Orgs:            types.ListNull(types.StringType),
					Repos:           mustStringList(t, ctx, []string{"https://gitlab.com/example-org/service-one"}),
					IgnoreDraftPrs:  types.BoolNull(),
					PrAction:        types.StringNull(),
					CommentContains: types.StringNull(),
				},
				UserAllowlist: types.ListNull(types.StringType),
			},
		},
	}

	wf, err := modelToWorkflow(ctx, m)
	if err != nil {
		t.Fatalf("modelToWorkflow() error: %v", err)
	}
	pr := wf.Triggers[0].GetGit().GetPullRequest()
	if pr == nil {
		t.Fatal("expected pull request trigger in proto")
	}
	if got, want := pr.GetRepos(), []string{"https://gitlab.com/example-org/service-one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("repos = %v, want %v", got, want)
	}
}

func TestModelToWorkflowAllowsSchemelessGitLabRepoOnly(t *testing.T) {
	ctx := context.Background()
	m := &platformWorkflowModel{
		Prompt: types.StringValue("review code"),
		Triggers: []triggerModel{
			{
				GitPullRequest: &gitPullRequestModel{
					Orgs:            types.ListNull(types.StringType),
					Repos:           mustStringList(t, ctx, []string{"gitlab.com/example-org/service-one"}),
					IgnoreDraftPrs:  types.BoolNull(),
					PrAction:        types.StringNull(),
					CommentContains: types.StringNull(),
				},
				UserAllowlist: types.ListNull(types.StringType),
			},
		},
	}

	wf, err := modelToWorkflow(ctx, m)
	if err != nil {
		t.Fatalf("modelToWorkflow() error: %v", err)
	}
	pr := wf.Triggers[0].GetGit().GetPullRequest()
	if pr == nil {
		t.Fatal("expected pull request trigger in proto")
	}
	if got, want := pr.GetRepos(), []string{"gitlab.com/example-org/service-one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("repos = %v, want %v", got, want)
	}
}

func TestGitRepoTargetProvider(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "legacy bare owner repo defaults to github",
			target: "example-org/service-one",
			want:   "github",
		},
		{
			name:   "schemeless gitlab host returns gitlab",
			target: "gitlab.com/example-org/service-one",
			want:   "gitlab",
		},
		{
			name:   "https gitlab host returns gitlab",
			target: "https://gitlab.com/example-org/service-one",
			want:   "gitlab",
		},
		{
			name:   "github enterprise host returns github",
			target: "github.enterprise.example.com/example-org/service-one",
			want:   "github",
		},
		{
			name:   "schemeless bitbucket host returns other",
			target: "bitbucket.org/example-org/service-one",
			want:   "other",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitRepoTargetProvider(tc.target); got != tc.want {
				t.Fatalf("gitRepoTargetProvider(%q) = %q, want %q", tc.target, got, tc.want)
			}
		})
	}
}

func TestGitRepoProviderFromHostname(t *testing.T) {
	cases := []struct {
		name     string
		hostname string
		want     string
	}{
		{
			name:     "github.com returns github",
			hostname: "github.com",
			want:     "github",
		},
		{
			name:     "github enterprise subdomain returns github",
			hostname: "github.enterprise.example.com",
			want:     "github",
		},
		{
			name:     "hostname containing github returns other",
			hostname: "git-mirror.internal.example.com",
			want:     "other",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitRepoProviderFromHostname(tc.hostname); got != tc.want {
				t.Fatalf("gitRepoProviderFromHostname(%q) = %q, want %q", tc.hostname, got, tc.want)
			}
		})
	}
}

func TestGitRepoTargetMetadata(t *testing.T) {
	cases := []struct {
		name         string
		target       string
		wantOwner    string
		wantProvider string
	}{
		{
			name:         "legacy bare owner repo",
			target:       "example-org/service-one",
			wantOwner:    "example-org",
			wantProvider: "github",
		},
		{
			name:         "schemeless gitlab host",
			target:       "gitlab.com/Example-Org/service-one",
			wantOwner:    "example-org",
			wantProvider: "gitlab",
		},
		{
			name:         "https github enterprise",
			target:       "https://github.enterprise.example.com/Org/repo",
			wantOwner:    "org",
			wantProvider: "github",
		},
		{
			name:         "known non github host",
			target:       "bitbucket.org/team/repo",
			wantOwner:    "team",
			wantProvider: "other",
		},
		{
			name:         "invalid target",
			target:       "not-a-repo",
			wantOwner:    "",
			wantProvider: "github",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gitRepoTargetMetadata(tc.target)
			if got.owner != tc.wantOwner || got.provider != tc.wantProvider {
				t.Fatalf(
					"gitRepoTargetMetadata(%q) = {owner:%q provider:%q}, want {owner:%q provider:%q}",
					tc.target,
					got.owner,
					got.provider,
					tc.wantOwner,
					tc.wantProvider,
				)
			}
		})
	}
}

func TestSlackTriggerCompletionReactionRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto_on", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("respond to slack"),
			Triggers: []triggerModel{
				{
					Slack: &slackTriggerModel{
						Channel:                       types.StringValue("C0123456789"),
						CompletionReactionMode:        types.StringValue("on"),
						CompletionReactionCustomEmoji: types.StringNull(),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		slack := wf.Triggers[0].GetSlackTrigger()
		if slack == nil {
			t.Fatal("expected slack trigger in proto")
		}
		if got, want := slack.GetSlackCompletionReactionMode(), v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_ON; got != want {
			t.Fatalf("completion reaction mode = %v, want %v", got, want)
		}
		if slack.SlackCompletionReactionCustomEmoji != nil {
			t.Fatalf("expected no custom emoji, got %q", slack.GetSlackCompletionReactionCustomEmoji())
		}
	})

	t.Run("model_to_proto_custom", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("respond to slack"),
			Triggers: []triggerModel{
				{
					Slack: &slackTriggerModel{
						Channel:                       types.StringValue("C0123456789"),
						CompletionReactionMode:        types.StringValue("custom"),
						CompletionReactionCustomEmoji: types.StringValue(":tada:"),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		slack := wf.Triggers[0].GetSlackTrigger()
		if got, want := slack.GetSlackCompletionReactionMode(), v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_CUSTOM; got != want {
			t.Fatalf("completion reaction mode = %v, want %v", got, want)
		}
		if got, want := slack.GetSlackCompletionReactionCustomEmoji(), ":tada:"; got != want {
			t.Fatalf("custom emoji = %q, want %q", got, want)
		}
	})

	t.Run("model_to_proto_custom_requires_emoji", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("respond to slack"),
			Triggers: []triggerModel{
				{
					Slack: &slackTriggerModel{
						Channel:                       types.StringValue("C0123456789"),
						CompletionReactionMode:        types.StringValue("custom"),
						CompletionReactionCustomEmoji: types.StringNull(),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		_, err := modelToWorkflow(ctx, m)
		if err == nil {
			t.Fatal("expected error when custom mode has no emoji")
		}
		if !strings.Contains(err.Error(), "completion_reaction_custom_emoji is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("model_to_proto_emoji_requires_custom_mode", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("respond to slack"),
			Triggers: []triggerModel{
				{
					Slack: &slackTriggerModel{
						Channel:                       types.StringValue("C0123456789"),
						CompletionReactionMode:        types.StringValue("on"),
						CompletionReactionCustomEmoji: types.StringValue(":tada:"),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		_, err := modelToWorkflow(ctx, m)
		if err == nil {
			t.Fatal("expected error when emoji set without custom mode")
		}
		if !strings.Contains(err.Error(), "can only be set when slack.completion_reaction_mode is \"custom\"") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("model_to_proto_invalid_mode", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("respond to slack"),
			Triggers: []triggerModel{
				{
					Slack: &slackTriggerModel{
						Channel:                types.StringValue("C0123456789"),
						CompletionReactionMode: types.StringValue("sometimes"),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		_, err := modelToWorkflow(ctx, m)
		if err == nil {
			t.Fatal("expected error for invalid completion_reaction_mode")
		}
		if !strings.Contains(err.Error(), "invalid slack.completion_reaction_mode") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("proto_to_model_custom", func(t *testing.T) {
		customEmoji := ":tada:"
		mode := v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_CUSTOM
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Prompts: []*v1.Prompt{{Prompt: "respond to slack"}},
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_SlackTrigger{
								SlackTrigger: &v1.SlackTrigger{
									Channel:                            "C0123456789",
									SlackCompletionReactionMode:        &mode,
									SlackCompletionReactionCustomEmoji: &customEmoji,
								},
							},
						},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		if model.Triggers[0].Slack == nil {
			t.Fatal("expected slack trigger in model")
		}
		if got, want := model.Triggers[0].Slack.CompletionReactionMode.ValueString(), "custom"; got != want {
			t.Fatalf("completion_reaction_mode = %q, want %q", got, want)
		}
		if got, want := model.Triggers[0].Slack.CompletionReactionCustomEmoji.ValueString(), ":tada:"; got != want {
			t.Fatalf("completion_reaction_custom_emoji = %q, want %q", got, want)
		}
	})

	t.Run("proto_to_model_unset_is_null", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Prompts: []*v1.Prompt{{Prompt: "respond to slack"}},
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_SlackTrigger{
								SlackTrigger: &v1.SlackTrigger{Channel: "C0123456789"},
							},
						},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		slack := model.Triggers[0].Slack
		if slack == nil {
			t.Fatal("expected slack trigger in model")
		}
		if !slack.CompletionReactionMode.IsNull() {
			t.Fatalf("expected completion_reaction_mode to be null, got %q", slack.CompletionReactionMode.ValueString())
		}
		if !slack.CompletionReactionCustomEmoji.IsNull() {
			t.Fatalf("expected completion_reaction_custom_emoji to be null, got %q", slack.CompletionReactionCustomEmoji.ValueString())
		}
	})
}

func TestLinearTriggerRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		statusIDs, _ := types.ListValueFrom(ctx, types.StringType, []string{"status-1", "status-2"})
		projectIDs, _ := types.ListValueFrom(ctx, types.StringType, []string{"project-1"})
		teamIDs, _ := types.ListValueFrom(ctx, types.StringType, []string{"team-1"})
		m := &platformWorkflowModel{
			Prompt: types.StringValue("handle linear events"),
			Triggers: []triggerModel{
				{
					Linear: &linearTriggerModel{
						StatusChanged: &linearStatusChangedModel{
							StatusIDs: statusIDs,
						},
						ProjectIDs: projectIDs,
						TeamIDs:    teamIDs,
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		if len(wf.Triggers) != 1 {
			t.Fatalf("expected 1 trigger, got %d", len(wf.Triggers))
		}
		linear := wf.Triggers[0].GetLinear()
		if linear == nil {
			t.Fatal("expected linear trigger in proto")
		}
		if linear.GetStatusChanged() == nil {
			t.Fatal("expected linear status_changed event in proto")
		}
		if len(linear.GetStatusChanged().GetStatusIds()) != 2 {
			t.Fatalf("expected 2 status_ids, got %d", len(linear.GetStatusChanged().GetStatusIds()))
		}
	})

	t.Run("proto_to_model", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				AutomationId: "test-id",
				Name:         "test-workflow",
				Enabled:      true,
				Workflow: &v1.Workflow{
					Prompts: []*v1.Prompt{{Prompt: "handle linear events"}},
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_Linear{
								Linear: &v1.LinearTrigger{
									Event: &v1.LinearTrigger_EndOfCycle{
										EndOfCycle: &v1.LinearEndOfCycleEvent{
											CycleIds: []string{"cycle-1"},
										},
									},
									ProjectIds: []string{"project-1"},
									TeamIds:    []string{"team-1"},
								},
							},
						},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		if len(model.Triggers) != 1 {
			t.Fatalf("expected 1 trigger, got %d", len(model.Triggers))
		}
		if model.Triggers[0].Linear == nil {
			t.Fatal("expected linear trigger in terraform model")
		}
		if model.Triggers[0].Linear.EndOfCycle == nil {
			t.Fatal("expected linear end_of_cycle event in terraform model")
		}
		if model.Triggers[0].Linear.ProjectIDs.IsNull() {
			t.Fatal("expected linear project_ids in terraform model")
		}
	})
}

// TestProtoToModelSetsUpdatedAt verifies that protoToModel correctly populates
// the UpdatedAt field from the proto response.
func TestProtoToModelSetsUpdatedAt(t *testing.T) {
	ctx := context.Background()

	input := &v1.AutomationWithOwner{
		Workflow: &v1.Automation{
			AutomationId: "test-id",
			Name:         "test-workflow",
			Enabled:      true,
			CreatedAt:    1700000000,
			UpdatedAt:    1700099999,
			Workflow: &v1.Workflow{
				Prompts: []*v1.Prompt{
					{Prompt: "do something"},
				},
				Triggers: []*v1.Trigger{
					{
						Trigger: &v1.Trigger_Cron{
							Cron: &v1.CronTrigger{Cron: "0 9 * * *"},
						},
					},
				},
			},
		},
	}

	model, err := protoToModel(ctx, input)
	if err != nil {
		t.Fatalf("protoToModel() error: %v", err)
	}

	if model.CreatedAt.ValueInt64() != 1700000000 {
		t.Errorf("CreatedAt = %d, want 1700000000", model.CreatedAt.ValueInt64())
	}
	if model.UpdatedAt.ValueInt64() != 1700099999 {
		t.Errorf("UpdatedAt = %d, want 1700099999", model.UpdatedAt.ValueInt64())
	}

	// Verify a different UpdatedAt value round-trips correctly (simulating
	// the server bumping the timestamp on update).
	input.Workflow.UpdatedAt = 1700199999
	model2, err := protoToModel(ctx, input)
	if err != nil {
		t.Fatalf("protoToModel() error on second call: %v", err)
	}
	if model2.UpdatedAt.ValueInt64() != 1700199999 {
		t.Errorf("UpdatedAt after update = %d, want 1700199999", model2.UpdatedAt.ValueInt64())
	}
}

func TestParseAutomationScope(t *testing.T) {
	t.Run("null_or_unknown", func(t *testing.T) {
		scope, err := parseAutomationScope(types.StringNull())
		if err != nil {
			t.Fatalf("parseAutomationScope(null) error: %v", err)
		}
		if scope != nil {
			t.Fatalf("expected nil scope for null input, got %v", *scope)
		}
	})

	t.Run("user", func(t *testing.T) {
		scope, err := parseAutomationScope(types.StringValue("user"))
		if err != nil {
			t.Fatalf("parseAutomationScope(user) error: %v", err)
		}
		if scope == nil || *scope != v1.AutomationScope_AUTOMATION_SCOPE_USER {
			t.Fatalf("expected user scope, got %+v", scope)
		}
	})

	t.Run("team_case_insensitive", func(t *testing.T) {
		scope, err := parseAutomationScope(types.StringValue("TEAM"))
		if err != nil {
			t.Fatalf("parseAutomationScope(TEAM) error: %v", err)
		}
		if scope == nil || *scope != v1.AutomationScope_AUTOMATION_SCOPE_TEAM {
			t.Fatalf("expected team scope, got %+v", scope)
		}
	})

	t.Run("team_editable", func(t *testing.T) {
		scope, err := parseAutomationScope(types.StringValue("team_editable"))
		if err != nil {
			t.Fatalf("parseAutomationScope(team_editable) error: %v", err)
		}
		if scope == nil || *scope != v1.AutomationScope_AUTOMATION_SCOPE_TEAM_EDITABLE {
			t.Fatalf("expected team_editable scope, got %+v", scope)
		}
	})

	t.Run("team_editable_user", func(t *testing.T) {
		scope, err := parseAutomationScope(types.StringValue("team_editable_user"))
		if err != nil {
			t.Fatalf("parseAutomationScope(team_editable_user) error: %v", err)
		}
		if scope == nil || *scope != v1.AutomationScope_AUTOMATION_SCOPE_TEAM_EDITABLE_USER {
			t.Fatalf("expected team_editable_user scope, got %+v", scope)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		_, err := parseAutomationScope(types.StringValue("workspace"))
		if err == nil {
			t.Fatal("expected error for invalid scope")
		}
	})
}

func TestProtoToModelSetsScope(t *testing.T) {
	ctx := context.Background()

	input := &v1.AutomationWithOwner{
		Workflow: &v1.Automation{
			AutomationId: "test-id",
			Name:         "test-workflow",
			Enabled:      true,
			Scope:        v1.AutomationScope_AUTOMATION_SCOPE_TEAM,
			Workflow: &v1.Workflow{
				Prompts: []*v1.Prompt{{Prompt: "do something"}},
				Triggers: []*v1.Trigger{
					{
						Trigger: &v1.Trigger_Cron{
							Cron: &v1.CronTrigger{Cron: "0 9 * * *"},
						},
					},
				},
			},
		},
	}

	model, err := protoToModel(ctx, input)
	if err != nil {
		t.Fatalf("protoToModel() error: %v", err)
	}
	if model.Scope.IsNull() || model.Scope.IsUnknown() {
		t.Fatal("expected scope to be set")
	}
	if got := model.Scope.ValueString(); got != "team" {
		t.Fatalf("scope = %q, want %q", got, "team")
	}
}

func TestAutomationScopeToModelTeamEditable(t *testing.T) {
	got := automationScopeToModel(v1.AutomationScope_AUTOMATION_SCOPE_TEAM_EDITABLE)
	if got.IsNull() || got.IsUnknown() {
		t.Fatal("expected team_editable scope string")
	}
	if got.ValueString() != "team_editable" {
		t.Fatalf("scope = %q, want %q", got.ValueString(), "team_editable")
	}
}

func TestAutomationScopeToModelTeamEditableUser(t *testing.T) {
	got := automationScopeToModel(v1.AutomationScope_AUTOMATION_SCOPE_TEAM_EDITABLE_USER)
	if got.IsNull() || got.IsUnknown() {
		t.Fatal("expected team_editable_user scope string")
	}
	if got.ValueString() != "team_editable_user" {
		t.Fatalf("scope = %q, want %q", got.ValueString(), "team_editable_user")
	}
}
