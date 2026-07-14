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

func TestGitCICompletedTriggerRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("triage CI failures"),
			Triggers: []triggerModel{
				{
					GitCICompleted: &gitCICompletedModel{
						Repos:              mustStringList(t, ctx, []string{"org/repo"}),
						Condition:          types.StringValue("failure"),
						IgnoreBaseFailures: types.BoolValue(true),
						Branch:             types.StringNull(),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		ci := wf.Triggers[0].GetGit().GetCiCompleted()
		if ci == nil {
			t.Fatal("expected ci_completed trigger in proto")
		}
		if got, want := ci.GetRepos(), []string{"org/repo"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("repos = %v, want %v", got, want)
		}
		if got, want := ci.GetCondition(), v1.GitCICompletionCondition_GIT_CI_COMPLETION_CONDITION_FAILURE; got != want {
			t.Fatalf("condition = %v, want %v", got, want)
		}
		if !ci.GetIgnoreBaseFailures() {
			t.Error("expected ignore_base_failures=true")
		}
		if ci.GetBranch() != "" {
			t.Fatalf("expected empty branch, got %q", ci.GetBranch())
		}
	})

	t.Run("model_to_proto_branch_mode", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("triage CI failures on main"),
			Triggers: []triggerModel{
				{
					GitCICompleted: &gitCICompletedModel{
						Repos:              mustStringList(t, ctx, []string{"org/repo"}),
						Condition:          types.StringNull(),
						IgnoreBaseFailures: types.BoolNull(),
						Branch:             types.StringValue("main"),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		ci := wf.Triggers[0].GetGit().GetCiCompleted()
		if ci == nil {
			t.Fatal("expected ci_completed trigger in proto")
		}
		if got, want := ci.GetBranch(), "main"; got != want {
			t.Fatalf("branch = %q, want %q", got, want)
		}
		if got, want := ci.GetCondition(), v1.GitCICompletionCondition_GIT_CI_COMPLETION_CONDITION_UNSPECIFIED; got != want {
			t.Fatalf("condition = %v, want %v", got, want)
		}
	})

	t.Run("model_to_proto_false_round_trip", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("triage CI failures"),
			Triggers: []triggerModel{
				{
					GitCICompleted: &gitCICompletedModel{
						Repos:              mustStringList(t, ctx, []string{"org/repo"}),
						IgnoreBaseFailures: types.BoolValue(false),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		model, err := protoToModel(ctx, &v1.AutomationWithOwner{Workflow: &v1.Automation{Workflow: wf}})
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		ci := model.Triggers[0].GitCICompleted
		if ci.IgnoreBaseFailures.IsNull() {
			t.Fatal("expected ignore_base_failures=false in model, got null")
		}
		if ci.IgnoreBaseFailures.ValueBool() {
			t.Error("expected ignore_base_failures=false in model")
		}
	})

	t.Run("model_to_proto_requires_repos", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("triage CI failures"),
			Triggers: []triggerModel{
				{
					GitCICompleted: &gitCICompletedModel{
						Repos: types.ListNull(types.StringType),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		_, err := modelToWorkflow(ctx, m)
		if err == nil {
			t.Fatal("expected error for missing repos")
		}
		if !strings.Contains(err.Error(), "git_ci_completed must specify at least one repo") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("model_to_proto_rejects_blank_repos", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("triage CI failures"),
			Triggers: []triggerModel{
				{
					GitCICompleted: &gitCICompletedModel{
						Repos: mustStringList(t, ctx, []string{"", "   "}),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		_, err := modelToWorkflow(ctx, m)
		if err == nil {
			t.Fatal("expected error for blank repos")
		}
		if !strings.Contains(err.Error(), "git_ci_completed must specify at least one repo") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("model_to_proto_invalid_condition", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("triage CI failures"),
			Triggers: []triggerModel{
				{
					GitCICompleted: &gitCICompletedModel{
						Repos:     mustStringList(t, ctx, []string{"org/repo"}),
						Condition: types.StringValue("sometimes"),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		_, err := modelToWorkflow(ctx, m)
		if err == nil {
			t.Fatal("expected error for invalid condition")
		}
		if !strings.Contains(err.Error(), "invalid git_ci_completed.condition") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("proto_to_model", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Prompts: []*v1.Prompt{{Prompt: "triage CI failures"}},
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_Git{
								Git: &v1.GitTrigger{
									Event: &v1.GitTrigger_CiCompleted{
										CiCompleted: &v1.GitCICompletedEvent{
											Repos:              []string{"org/repo"},
											Condition:          v1.GitCICompletionCondition_GIT_CI_COMPLETION_CONDITION_SUCCESS,
											IgnoreBaseFailures: true,
											Branch:             "main",
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
		ci := model.Triggers[0].GitCICompleted
		if ci == nil {
			t.Fatal("expected git_ci_completed trigger in model")
		}
		var repos []string
		diags := ci.Repos.ElementsAs(ctx, &repos, false)
		if diags.HasError() {
			t.Fatalf("failed to read repos from model: %v", diags)
		}
		if got, want := repos, []string{"org/repo"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("repos = %v, want %v", got, want)
		}
		if got, want := ci.Condition.ValueString(), "success"; got != want {
			t.Fatalf("condition = %q, want %q", got, want)
		}
		if !ci.IgnoreBaseFailures.ValueBool() {
			t.Error("expected ignore_base_failures=true")
		}
		if got, want := ci.Branch.ValueString(), "main"; got != want {
			t.Fatalf("branch = %q, want %q", got, want)
		}
	})

	t.Run("proto_to_model_defaults", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_Git{
								Git: &v1.GitTrigger{
									Event: &v1.GitTrigger_CiCompleted{
										CiCompleted: &v1.GitCICompletedEvent{
											Repos: []string{"org/repo"},
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
		ci := model.Triggers[0].GitCICompleted
		if ci == nil {
			t.Fatal("expected git_ci_completed trigger in model")
		}
		if !ci.Condition.IsNull() {
			t.Fatalf("expected condition to be null, got %q", ci.Condition.ValueString())
		}
		if ci.IgnoreBaseFailures.IsNull() {
			t.Fatal("expected ignore_base_failures=false in model, got null")
		}
		if ci.IgnoreBaseFailures.ValueBool() {
			t.Error("expected ignore_base_failures=false in model")
		}
		if !ci.Branch.IsNull() {
			t.Fatalf("expected branch to be null, got %q", ci.Branch.ValueString())
		}
	})
}

func TestPreserveEquivalentGitCICompletionConditions(t *testing.T) {
	state := &platformWorkflowModel{
		Triggers: []triggerModel{
			{
				GitCICompleted: &gitCICompletedModel{
					Condition: types.StringValue("failure"),
				},
			},
		},
	}
	reference := platformWorkflowModel{
		Triggers: []triggerModel{
			{
				GitCICompleted: &gitCICompletedModel{
					Condition: types.StringValue("Failure"),
				},
			},
		},
	}

	preserveEquivalentGitCICompletionConditions(state, reference)

	if got, want := state.Triggers[0].GitCICompleted.Condition.ValueString(), "Failure"; got != want {
		t.Fatalf("condition = %q, want %q", got, want)
	}
}

func TestTriggerModelToProtoRejectsMultipleTriggerTypes(t *testing.T) {
	ctx := context.Background()
	m := &platformWorkflowModel{
		Prompt: types.StringValue("review code"),
		Triggers: []triggerModel{
			{
				GitCICompleted: &gitCICompletedModel{
					Repos: mustStringList(t, ctx, []string{"org/repo"}),
				},
				GitPush: &gitPushModel{
					Repo: types.StringValue("org/repo"),
				},
				UserAllowlist: types.ListNull(types.StringType),
			},
		},
	}

	_, err := modelToWorkflow(ctx, m)
	if err == nil {
		t.Fatal("expected error when multiple trigger types are set")
	}
	if !strings.Contains(err.Error(), "must specify exactly one of git_pull_request, git_push, git_ci_completed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGitPullRequestCommentContainsIsRegexRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto_true", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("respond to comments"),
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs:                   types.ListNull(types.StringType),
						Repos:                  mustStringList(t, ctx, []string{"org/repo"}),
						IgnoreDraftPrs:         types.BoolNull(),
						PrAction:               types.StringValue("commented"),
						CommentContains:        types.StringValue(`^/(review|fix)\b`),
						CommentContainsIsRegex: types.BoolValue(true),
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
		if !pr.GetCommentContainsIsRegex() {
			t.Error("expected comment_contains_is_regex=true on proto")
		}
	})

	t.Run("model_to_proto_false_round_trip", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("respond to comments"),
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs:                   types.ListNull(types.StringType),
						Repos:                  mustStringList(t, ctx, []string{"org/repo"}),
						CommentContains:        types.StringValue("please review"),
						CommentContainsIsRegex: types.BoolValue(false),
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
		if pr.GetCommentContainsIsRegex() {
			t.Error("expected comment_contains_is_regex=false on proto")
		}
		model, err := protoToModel(ctx, &v1.AutomationWithOwner{Workflow: &v1.Automation{Workflow: wf}})
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		modelPr := model.Triggers[0].GitPullRequest
		if modelPr.CommentContainsIsRegex.IsNull() {
			t.Fatal("expected comment_contains_is_regex=false in model, got null")
		}
		if modelPr.CommentContainsIsRegex.ValueBool() {
			t.Error("expected comment_contains_is_regex=false in model")
		}
	})

	t.Run("proto_to_model_round_trip", func(t *testing.T) {
		event := &v1.GitPullRequestEvent{
			Repos:                  []string{"org/repo"},
			CommentContains:        `^/(review|fix)\b`,
			CommentContainsIsRegex: true,
		}

		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_Git{
								Git: &v1.GitTrigger{
									Event: &v1.GitTrigger_PullRequest{PullRequest: event},
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
		pr := model.Triggers[0].GitPullRequest
		if pr == nil {
			t.Fatal("expected git_pull_request trigger in model")
		}
		if !pr.CommentContainsIsRegex.ValueBool() {
			t.Error("expected comment_contains_is_regex=true in model")
		}
	})

	t.Run("proto_to_model_default_false", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Triggers: []*v1.Trigger{
						{
							Trigger: &v1.Trigger_Git{
								Git: &v1.GitTrigger{
									Event: &v1.GitTrigger_PullRequest{
										PullRequest: &v1.GitPullRequestEvent{
											Repos: []string{"org/repo"},
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
		if model.Triggers[0].GitPullRequest.CommentContainsIsRegex.IsNull() {
			t.Fatal("expected comment_contains_is_regex=false in model, got null")
		}
		if model.Triggers[0].GitPullRequest.CommentContainsIsRegex.ValueBool() {
			t.Fatal("expected comment_contains_is_regex=false in model")
		}
	})
}

func TestMcpActionServerIDRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("use mcp"),
			Triggers: []triggerModel{
				{
					Cron:          &cronModel{Schedule: types.StringValue("0 9 * * *")},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
			Actions: []actionModel{
				{
					Mcp: &mcpActionModel{
						Server:   types.StringValue("my-server"),
						ServerID: types.Int64Value(42),
					},
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		server := wf.Actions[0].GetMcp().GetServer()
		if server == nil {
			t.Fatal("expected mcp server config in proto")
		}
		if got, want := server.GetName(), "my-server"; got != want {
			t.Fatalf("server name = %q, want %q", got, want)
		}
		if server.Id == nil {
			t.Fatal("expected server id to be set on proto")
		}
		if got := server.GetId(); got != 42 {
			t.Fatalf("server id = %d, want 42", got)
		}
	})

	t.Run("model_to_proto_null_id_is_omitted", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("use mcp"),
			Triggers: []triggerModel{
				{
					Cron:          &cronModel{Schedule: types.StringValue("0 9 * * *")},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
			Actions: []actionModel{
				{
					Mcp: &mcpActionModel{
						Server:   types.StringValue("my-server"),
						ServerID: types.Int64Null(),
					},
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		if wf.Actions[0].GetMcp().GetServer().Id != nil {
			t.Fatal("expected server id to be unset on proto")
		}
	})

	t.Run("proto_to_model_round_trip", func(t *testing.T) {
		id := int64(42)
		server := &v1.McpServerConfig{Name: "my-server", Id: &id}

		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Actions: []*v1.Action{
						{Action: &v1.Action_Mcp{
							Mcp: &v1.McpAction{Server: server},
						}},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		mcp := model.Actions[0].Mcp
		if mcp == nil {
			t.Fatal("expected mcp action in model")
		}
		if got, want := mcp.Server.ValueString(), "my-server"; got != want {
			t.Fatalf("server = %q, want %q", got, want)
		}
		if got, want := mcp.ServerID.ValueInt64(), int64(42); got != want {
			t.Fatalf("server_id = %d, want %d", got, want)
		}
	})

	t.Run("proto_to_model_unset_id_is_null", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Actions: []*v1.Action{
						{Action: &v1.Action_Mcp{
							Mcp: &v1.McpAction{
								Server: &v1.McpServerConfig{Name: "my-server"},
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
		if !model.Actions[0].Mcp.ServerID.IsNull() {
			t.Fatal("expected server_id to be null when unset")
		}
	})
}

func TestMcpActionServerIDIsOptionalComputed(t *testing.T) {
	r := &platformWorkflowResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, schemaResp)

	attr, ok := schemaResp.Schema.Attributes["action"]
	if !ok {
		t.Fatal("schema is missing action attribute")
	}
	actionAttr, ok := attr.(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("action is not a ListNestedAttribute, got %T", attr)
	}
	mcpAttrRaw, ok := actionAttr.NestedObject.Attributes["mcp"]
	if !ok {
		t.Fatal("schema is missing action.mcp attribute")
	}
	mcpAttr, ok := mcpAttrRaw.(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("action.mcp is not a SingleNestedAttribute, got %T", mcpAttrRaw)
	}
	serverIDAttrRaw, ok := mcpAttr.Attributes["server_id"]
	if !ok {
		t.Fatal("schema is missing action.mcp.server_id attribute")
	}
	serverIDAttr, ok := serverIDAttrRaw.(schema.Int64Attribute)
	if !ok {
		t.Fatalf("action.mcp.server_id is not an Int64Attribute, got %T", serverIDAttrRaw)
	}

	if !serverIDAttr.Optional {
		t.Fatal("action.mcp.server_id should be Optional")
	}
	if !serverIDAttr.Computed {
		t.Fatal("action.mcp.server_id should be Computed")
	}
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

// TestModelIsOptionalComputed verifies that the model attribute is
// Optional+Computed with UseStateForUnknown, so a server-assigned default
// model is legitimate state instead of an "inconsistent result after apply"
// error followed by permanent plan drift.
func TestModelIsOptionalComputed(t *testing.T) {
	r := &platformWorkflowResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, schemaResp)

	attr, ok := schemaResp.Schema.Attributes["model"]
	if !ok {
		t.Fatal("schema is missing model attribute")
	}

	strAttr, ok := attr.(schema.StringAttribute)
	if !ok {
		t.Fatalf("model is not a StringAttribute, got %T", attr)
	}

	if !strAttr.Optional {
		t.Error("model should be Optional")
	}
	if !strAttr.Computed {
		t.Error("model should be Computed (the server assigns a default when unset)")
	}
	if len(strAttr.PlanModifiers) == 0 {
		t.Error("model should have a UseStateForUnknown plan modifier")
	}
}

// TestSlackCompletionReactionModeIsOptionalComputed guards against the
// round-trip bug where the API reports the default mode ("on") for configs
// that leave completion_reaction_mode unset: without Computed, applies fail
// with "Provider produced inconsistent result after apply" and the field
// drifts on every subsequent plan.
func TestSlackCompletionReactionModeIsOptionalComputed(t *testing.T) {
	r := &platformWorkflowResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, schemaResp)

	trigger, ok := schemaResp.Schema.Attributes["trigger"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatal("schema is missing trigger list attribute")
	}
	slack, ok := trigger.NestedObject.Attributes["slack"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatal("trigger schema is missing slack attribute")
	}
	mode, ok := slack.Attributes["completion_reaction_mode"].(schema.StringAttribute)
	if !ok {
		t.Fatal("slack trigger schema is missing completion_reaction_mode")
	}

	if !mode.Optional {
		t.Error("completion_reaction_mode should be Optional")
	}
	if !mode.Computed {
		t.Error("completion_reaction_mode should be Computed (the server reports the default \"on\" when unset)")
	}
}

// TestSlackCompletionReactionUnknownModeSkipped verifies that an unknown
// (computed, not yet decided) mode does not get sent to the API and does not
// fail validation.
func TestSlackCompletionReactionUnknownModeSkipped(t *testing.T) {
	st := &v1.SlackTrigger{}
	if err := applySlackCompletionReaction(st, types.StringUnknown(), types.StringNull()); err != nil {
		t.Fatalf("unknown mode should be skipped, got error: %v", err)
	}
	if st.SlackCompletionReactionMode != nil {
		t.Error("unknown mode should not set SlackCompletionReactionMode on the proto")
	}
}

// TestModelUnsetAcceptsServerDefault verifies that when the practitioner
// leaves model unset, the provider sends no model to the server, and the
// server-assigned default in the response is written to state.
func TestModelUnsetAcceptsServerDefault(t *testing.T) {
	ctx := context.Background()

	m := &platformWorkflowModel{
		Prompt: types.StringValue("do something"),
		Model:  types.StringNull(),
		Triggers: []triggerModel{
			{
				Cron:          &cronModel{Schedule: types.StringValue("0 9 * * *")},
				UserAllowlist: types.ListNull(types.StringType),
			},
		},
	}

	wf, err := modelToWorkflow(ctx, m)
	if err != nil {
		t.Fatalf("modelToWorkflow() error: %v", err)
	}
	if wf.Model != nil {
		t.Fatalf("expected no model in proto when unset, got %q", wf.GetModel())
	}

	// During create the plan value is unknown (Computed with no prior state);
	// it must not be sent either.
	m.Model = types.StringUnknown()
	wf, err = modelToWorkflow(ctx, m)
	if err != nil {
		t.Fatalf("modelToWorkflow() error: %v", err)
	}
	if wf.Model != nil {
		t.Fatalf("expected no model in proto when unknown, got %q", wf.GetModel())
	}

	// The server backfills a default model; the response value becomes state.
	serverModel := "server-default-model"
	response := &v1.AutomationWithOwner{
		Workflow: &v1.Automation{
			AutomationId: "test-id",
			Workflow: &v1.Workflow{
				Prompts: []*v1.Prompt{{Prompt: "do something"}},
				Model:   &serverModel,
			},
		},
	}
	state, err := protoToModel(ctx, response)
	if err != nil {
		t.Fatalf("protoToModel() error: %v", err)
	}
	if state.Model.IsNull() || state.Model.IsUnknown() {
		t.Fatal("expected server-assigned model in state")
	}
	if got := state.Model.ValueString(); got != serverModel {
		t.Fatalf("model = %q, want %q", got, serverModel)
	}
}

// TestGitConfigReposPopulatedFromTriggers verifies that repos referenced by
// git triggers are mirrored into GitConfig.Repos, so automations whose only
// repo references live in their triggers still persist git configuration.
func TestGitConfigReposPopulatedFromTriggers(t *testing.T) {
	ctx := context.Background()

	t.Run("trigger_repos_without_git_repo", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("review code"),
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs:  types.ListNull(types.StringType),
						Repos: mustStringList(t, ctx, []string{"example-org/repo-one", "example-org/repo-two"}),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
				{
					GitPush: &gitPushModel{
						Repo:   types.StringValue("example-org/repo-three"),
						Branch: types.StringNull(),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		if wf.GitConfig == nil {
			t.Fatal("expected GitConfig to be populated from trigger repos")
		}
		want := []string{"example-org/repo-one", "example-org/repo-two", "example-org/repo-three"}
		if got := wf.GitConfig.GetRepos(); !reflect.DeepEqual(got, want) {
			t.Fatalf("GitConfig.Repos = %v, want %v", got, want)
		}
		if got := wf.GitConfig.GetRepo(); got != "" {
			t.Fatalf("GitConfig.Repo = %q, want empty (git_repo unset)", got)
		}
	})

	t.Run("git_repo_included_first_and_deduplicated", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt:  types.StringValue("review code"),
			GitRepo: types.StringValue("github.com/example-org/repo-one"),
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs: types.ListNull(types.StringType),
						// repo-one is spelled differently but refers to the
						// same repository as git_repo.
						Repos: mustStringList(t, ctx, []string{"example-org/repo-one", "example-org/repo-two"}),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		if wf.GitConfig == nil {
			t.Fatal("expected GitConfig to be populated")
		}
		if got, want := wf.GitConfig.GetRepo(), "github.com/example-org/repo-one"; got != want {
			t.Fatalf("GitConfig.Repo = %q, want %q", got, want)
		}
		want := []string{"github.com/example-org/repo-one", "example-org/repo-two"}
		if got := wf.GitConfig.GetRepos(); !reflect.DeepEqual(got, want) {
			t.Fatalf("GitConfig.Repos = %v, want %v", got, want)
		}
	})

	t.Run("org_only_trigger_sets_no_git_config", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("review code"),
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs:  mustStringList(t, ctx, []string{"example-org"}),
						Repos: types.ListNull(types.StringType),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		if wf.GitConfig != nil {
			t.Fatalf("expected no GitConfig for org-only trigger, got %+v", wf.GitConfig)
		}
	})

	t.Run("duplicate_repos_across_triggers_deduplicated", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("review code"),
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs:  types.ListNull(types.StringType),
						Repos: mustStringList(t, ctx, []string{"example-org/repo-one"}),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
				{
					GitPush: &gitPushModel{
						Repo:   types.StringValue("Example-Org/Repo-One"),
						Branch: types.StringNull(),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		if wf.GitConfig == nil {
			t.Fatal("expected GitConfig to be populated")
		}
		want := []string{"example-org/repo-one"}
		if got := wf.GitConfig.GetRepos(); !reflect.DeepEqual(got, want) {
			t.Fatalf("GitConfig.Repos = %v, want %v", got, want)
		}
	})

	t.Run("same_owner_repo_on_different_hosts_preserved", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("review code"),
			Triggers: []triggerModel{
				{
					GitPullRequest: &gitPullRequestModel{
						Orgs:  types.ListNull(types.StringType),
						Repos: mustStringList(t, ctx, []string{"github.com/example-org/repo-one"}),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
				{
					GitPush: &gitPushModel{
						Repo:   types.StringValue("gitlab.com/example-org/repo-one"),
						Branch: types.StringNull(),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		if wf.GitConfig == nil {
			t.Fatal("expected GitConfig to be populated")
		}
		want := []string{"github.com/example-org/repo-one", "gitlab.com/example-org/repo-one"}
		if got := wf.GitConfig.GetRepos(); !reflect.DeepEqual(got, want) {
			t.Fatalf("GitConfig.Repos = %v, want %v", got, want)
		}
	})
}

// TestGitConfigReposRoundTripStable verifies that sending trigger-derived
// GitConfig.Repos does not introduce drift: reading back a server response
// that echoes those repos leaves git_repo/git_branch null, and converting the
// resulting state again produces the same GitConfig.
func TestGitConfigReposRoundTripStable(t *testing.T) {
	ctx := context.Background()

	m := &platformWorkflowModel{
		Prompt: types.StringValue("review code"),
		Triggers: []triggerModel{
			{
				GitPullRequest: &gitPullRequestModel{
					Orgs:  types.ListNull(types.StringType),
					Repos: mustStringList(t, ctx, []string{"example-org/repo-one"}),
				},
				UserAllowlist: types.ListNull(types.StringType),
			},
		},
	}

	wf, err := modelToWorkflow(ctx, m)
	if err != nil {
		t.Fatalf("modelToWorkflow() error: %v", err)
	}
	if wf.GitConfig == nil || len(wf.GitConfig.GetRepos()) == 0 {
		t.Fatal("expected GitConfig.Repos to be populated")
	}

	// Simulate the server echoing the stored workflow back.
	response := &v1.AutomationWithOwner{
		Workflow: &v1.Automation{
			AutomationId: "test-id",
			Workflow:     wf,
		},
	}
	state, err := protoToModel(ctx, response)
	if err != nil {
		t.Fatalf("protoToModel() error: %v", err)
	}
	if !state.GitRepo.IsNull() {
		t.Fatalf("expected git_repo to stay null, got %q", state.GitRepo.ValueString())
	}
	if !state.GitBranch.IsNull() {
		t.Fatalf("expected git_branch to stay null, got %q", state.GitBranch.ValueString())
	}

	wf2, err := modelToWorkflow(ctx, &state)
	if err != nil {
		t.Fatalf("modelToWorkflow() error on round-tripped state: %v", err)
	}
	if wf2.GitConfig == nil {
		t.Fatal("expected GitConfig after round trip")
	}
	if got, want := wf2.GitConfig.GetRepos(), wf.GitConfig.GetRepos(); !reflect.DeepEqual(got, want) {
		t.Fatalf("GitConfig.Repos after round trip = %v, want %v", got, want)
	}
	if got, want := wf2.GitConfig.GetRepo(), wf.GitConfig.GetRepo(); got != want {
		t.Fatalf("GitConfig.Repo after round trip = %q, want %q", got, want)
	}
}

// TestEnvironmentPublicIDRoundTrip verifies that agent_options.environment_public_id
// round-trips through modelToWorkflow -> proto -> protoToModel, and that an unset
// value stays null rather than becoming an empty string.
func TestEnvironmentPublicIDRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("set_value_round_trips", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt:              types.StringValue("do the thing"),
			EnvironmentPublicID: types.StringValue("env-abc123"),
			Triggers: []triggerModel{
				{Webhook: &webhookTriggerModel{}},
			},
		}

		workflow, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error = %v", err)
		}
		if got := workflow.GetAgentOptions().GetEnvironmentPublicId(); got != "env-abc123" {
			t.Fatalf("AgentOptions.EnvironmentPublicId = %q, want %q", got, "env-abc123")
		}

		out, err := protoToModel(ctx, &v1.AutomationWithOwner{
			Workflow: &v1.Automation{Workflow: workflow},
		})
		if err != nil {
			t.Fatalf("protoToModel() error = %v", err)
		}
		if out.EnvironmentPublicID.IsNull() || out.EnvironmentPublicID.ValueString() != "env-abc123" {
			t.Fatalf("EnvironmentPublicID = %v, want %q", out.EnvironmentPublicID, "env-abc123")
		}
	})

	t.Run("unset_value_stays_null", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("do the thing"),
			Triggers: []triggerModel{
				{Webhook: &webhookTriggerModel{}},
			},
		}

		workflow, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error = %v", err)
		}
		if workflow.GetAgentOptions() != nil {
			t.Fatalf("AgentOptions = %v, want nil when no agent options are set", workflow.GetAgentOptions())
		}

		out, err := protoToModel(ctx, &v1.AutomationWithOwner{
			Workflow: &v1.Automation{Workflow: workflow},
		})
		if err != nil {
			t.Fatalf("protoToModel() error = %v", err)
		}
		if !out.EnvironmentPublicID.IsNull() {
			t.Fatalf("EnvironmentPublicID = %v, want null", out.EnvironmentPublicID)
		}
	})
}

func TestPreserveEquivalentEnvironmentPublicID(t *testing.T) {
	t.Run("preserves_reference_formatting_for_equivalent_value", func(t *testing.T) {
		state := &platformWorkflowModel{
			EnvironmentPublicID: types.StringValue("env-abc123"),
		}
		reference := platformWorkflowModel{
			EnvironmentPublicID: types.StringValue("  env-abc123  "),
		}

		preserveEquivalentEnvironmentPublicID(state, reference)

		if got := state.EnvironmentPublicID.ValueString(); got != "  env-abc123  " {
			t.Fatalf("EnvironmentPublicID = %q, want %q", got, "  env-abc123  ")
		}
	})

	t.Run("leaves_state_unchanged_for_non_equivalent_value", func(t *testing.T) {
		state := &platformWorkflowModel{
			EnvironmentPublicID: types.StringValue("env-abc123"),
		}
		reference := platformWorkflowModel{
			EnvironmentPublicID: types.StringValue("env-different"),
		}

		preserveEquivalentEnvironmentPublicID(state, reference)

		if got := state.EnvironmentPublicID.ValueString(); got != "env-abc123" {
			t.Fatalf("EnvironmentPublicID = %q, want %q", got, "env-abc123")
		}
	})
}
