package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/cursor/terraform-provider-cursor/internal/proto/v1"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	datasourceschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
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

func mustStringMap(t *testing.T, ctx context.Context, values map[string]string) types.Map {
	t.Helper()

	value, diags := types.MapValueFrom(ctx, types.StringType, values)
	if diags.HasError() {
		t.Fatalf("failed to build string map: %v", diags)
	}
	return value
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

	t.Run("empty_configured_value_does_not_drift_to_null", func(t *testing.T) {
		state := &platformWorkflowModel{
			EnvironmentPublicID: types.StringNull(),
		}
		reference := platformWorkflowModel{
			EnvironmentPublicID: types.StringValue("   "),
		}

		preserveEquivalentEnvironmentPublicID(state, reference)

		if state.EnvironmentPublicID.IsNull() || state.EnvironmentPublicID.ValueString() != "   " {
			t.Fatalf("EnvironmentPublicID = %v, want %q", state.EnvironmentPublicID, "   ")
		}
	})

	t.Run("null_state_with_real_reference_value_stays_null", func(t *testing.T) {
		state := &platformWorkflowModel{
			EnvironmentPublicID: types.StringNull(),
		}
		reference := platformWorkflowModel{
			EnvironmentPublicID: types.StringValue("env-abc123"),
		}

		preserveEquivalentEnvironmentPublicID(state, reference)

		if !state.EnvironmentPublicID.IsNull() {
			t.Fatalf("EnvironmentPublicID = %v, want null", state.EnvironmentPublicID)
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

func TestPrivateWorkerSchemaParity(t *testing.T) {
	resourceResponse := &resource.SchemaResponse{}
	(&platformWorkflowResource{}).Schema(context.Background(), resource.SchemaRequest{}, resourceResponse)

	resourceAttribute, ok := resourceResponse.Schema.Attributes["private_worker"]
	if !ok {
		t.Fatal("resource schema is missing private_worker")
	}
	resourcePrivateWorker, ok := resourceAttribute.(schema.SingleNestedAttribute)
	if !ok || !resourcePrivateWorker.Optional {
		t.Fatalf("resource private_worker = %#v, want optional SingleNestedAttribute", resourceAttribute)
	}
	resourceLabels, ok := resourcePrivateWorker.Attributes["labels"].(schema.MapAttribute)
	if !ok || !resourceLabels.Optional || !resourceLabels.Computed || resourceLabels.Default == nil || resourceLabels.ElementType != types.StringType {
		t.Fatalf("resource private_worker.labels = %#v, want optional+computed map(string) with an empty default", resourcePrivateWorker.Attributes["labels"])
	}

	dataSourceResponse := &datasource.SchemaResponse{}
	(&platformWorkflowDataSource{}).Schema(context.Background(), datasource.SchemaRequest{}, dataSourceResponse)

	dataSourceAttribute, ok := dataSourceResponse.Schema.Attributes["private_worker"]
	if !ok {
		t.Fatal("data source schema is missing private_worker")
	}
	dataSourcePrivateWorker, ok := dataSourceAttribute.(datasourceschema.SingleNestedAttribute)
	if !ok || !dataSourcePrivateWorker.Computed {
		t.Fatalf("data source private_worker = %#v, want computed SingleNestedAttribute", dataSourceAttribute)
	}
	dataSourceLabels, ok := dataSourcePrivateWorker.Attributes["labels"].(datasourceschema.MapAttribute)
	if !ok || !dataSourceLabels.Computed || dataSourceLabels.ElementType != types.StringType {
		t.Fatalf("data source private_worker.labels = %#v, want computed map(string)", dataSourcePrivateWorker.Attributes["labels"])
	}
}

func TestPrivateWorkerRoundTrip(t *testing.T) {
	ctx := context.Background()
	wantLabels := map[string]string{
		"repo": "example-org/example-repo",
		"pool": "example-production-pool",
	}
	skipInstall := false

	model := &platformWorkflowModel{
		Prompt:              types.StringValue("review requests"),
		SkipInstall:         types.BoolValue(skipInstall),
		EnvironmentPublicID: types.StringValue("environment-id"),
		PrivateWorker: &privateWorkerModel{
			Labels: mustStringMap(t, ctx, wantLabels),
		},
		Triggers: []triggerModel{{Webhook: &webhookTriggerModel{}}},
	}

	workflow, err := modelToWorkflow(ctx, model)
	if err != nil {
		t.Fatalf("modelToWorkflow() error = %v", err)
	}
	if workflow.GetAgentOptions().GetPrivateWorker() == nil {
		t.Fatal("AgentOptions.PrivateWorker is nil")
	}
	gotProtoLabels := workflow.GetAgentOptions().GetPrivateWorker().GetLabels()
	if len(gotProtoLabels) != 2 || gotProtoLabels[0].GetKey() != "pool" || gotProtoLabels[1].GetKey() != "repo" {
		t.Fatalf("private worker labels are not serialized deterministically: %#v", gotProtoLabels)
	}
	if workflow.GetAgentOptions().SkipInstall == nil || workflow.GetAgentOptions().GetSkipInstall() != skipInstall {
		t.Fatal("adding private_worker clobbered skip_install")
	}
	if got := workflow.GetAgentOptions().GetEnvironmentPublicId(); got != "environment-id" {
		t.Fatalf("adding private_worker clobbered environment_public_id: got %q", got)
	}

	state, err := protoToModel(ctx, &v1.AutomationWithOwner{
		Workflow: &v1.Automation{Workflow: workflow},
	})
	if err != nil {
		t.Fatalf("protoToModel() error = %v", err)
	}
	if state.PrivateWorker == nil {
		t.Fatal("PrivateWorker is nil after round trip")
	}
	var gotLabels map[string]string
	diags := state.PrivateWorker.Labels.ElementsAs(ctx, &gotLabels, false)
	if diags.HasError() {
		t.Fatalf("failed to read private worker labels: %v", diags)
	}
	if !reflect.DeepEqual(gotLabels, wantLabels) {
		t.Fatalf("private worker labels = %#v, want %#v", gotLabels, wantLabels)
	}

	state.Prompt = types.StringValue("updated review prompt")
	updatedWorkflow, err := modelToWorkflow(ctx, &state)
	if err != nil {
		t.Fatalf("modelToWorkflow() after unrelated update error = %v", err)
	}
	updatedLabels := updatedWorkflow.GetAgentOptions().GetPrivateWorker().GetLabels()
	if len(updatedLabels) != 2 || updatedLabels[0].GetKey() != "pool" || updatedLabels[1].GetKey() != "repo" {
		t.Fatalf("unrelated update did not retain private worker labels: %#v", updatedLabels)
	}
}

func TestPrivateWorkerPresenceSemantics(t *testing.T) {
	ctx := context.Background()

	t.Run("absent_uses_default_worker_routing", func(t *testing.T) {
		workflow, err := modelToWorkflow(ctx, &platformWorkflowModel{
			Prompt:   types.StringValue("do the thing"),
			Triggers: []triggerModel{{Webhook: &webhookTriggerModel{}}},
		})
		if err != nil {
			t.Fatalf("modelToWorkflow() error = %v", err)
		}
		if workflow.GetAgentOptions() != nil {
			t.Fatalf("AgentOptions = %#v, want nil", workflow.GetAgentOptions())
		}
	})

	t.Run("removal_preserves_other_agent_options", func(t *testing.T) {
		workflow, err := modelToWorkflow(ctx, &platformWorkflowModel{
			Prompt:      types.StringValue("do the thing"),
			SkipInstall: types.BoolValue(true),
			Triggers:    []triggerModel{{Webhook: &webhookTriggerModel{}}},
		})
		if err != nil {
			t.Fatalf("modelToWorkflow() error = %v", err)
		}
		if workflow.GetAgentOptions() == nil || !workflow.GetAgentOptions().GetSkipInstall() {
			t.Fatal("removing private_worker clobbered skip_install")
		}
		if workflow.GetAgentOptions().GetPrivateWorker() != nil {
			t.Fatalf("PrivateWorker = %#v, want nil", workflow.GetAgentOptions().GetPrivateWorker())
		}
	})

	t.Run("present_empty_targets_any_private_worker", func(t *testing.T) {
		workflow, err := modelToWorkflow(ctx, &platformWorkflowModel{
			Prompt: types.StringValue("do the thing"),
			PrivateWorker: &privateWorkerModel{
				Labels: mustStringMap(t, ctx, map[string]string{}),
			},
			Triggers: []triggerModel{{Webhook: &webhookTriggerModel{}}},
		})
		if err != nil {
			t.Fatalf("modelToWorkflow() error = %v", err)
		}
		if workflow.GetAgentOptions().GetPrivateWorker() == nil {
			t.Fatal("PrivateWorker is nil for a present empty object")
		}
		if got := workflow.GetAgentOptions().GetPrivateWorker().GetLabels(); len(got) != 0 {
			t.Fatalf("PrivateWorker.Labels = %#v, want empty", got)
		}

		state, err := protoToModel(ctx, &v1.AutomationWithOwner{
			Workflow: &v1.Automation{Workflow: workflow},
		})
		if err != nil {
			t.Fatalf("protoToModel() error = %v", err)
		}
		if state.PrivateWorker == nil || state.PrivateWorker.Labels.IsNull() {
			t.Fatal("present empty PrivateWorker did not survive round trip")
		}
	})
}

func TestPrivateWorkerAPILabelOrderDoesNotDrift(t *testing.T) {
	ctx := context.Background()
	state, err := protoToModel(ctx, &v1.AutomationWithOwner{
		Workflow: &v1.Automation{Workflow: &v1.Workflow{
			AgentOptions: &v1.AgentOptions{
				PrivateWorker: &v1.AgentPrivateWorkerConfig{Labels: []*v1.AgentPrivateWorkerLabel{
					{Key: "repo", Value: "example-org/example-repo"},
					{Key: "pool", Value: "example-production-pool"},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("protoToModel() error = %v", err)
	}

	workflow, err := modelToWorkflow(ctx, &state)
	if err != nil {
		t.Fatalf("modelToWorkflow() error = %v", err)
	}
	labels := workflow.GetAgentOptions().GetPrivateWorker().GetLabels()
	if len(labels) != 2 || labels[0].GetKey() != "pool" || labels[1].GetKey() != "repo" {
		t.Fatalf("private worker labels were not canonicalized: %#v", labels)
	}
}

func TestSlackChannelCreatedTriggerRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("welcome new channels"),
			Triggers: []triggerModel{
				{
					SlackChannelCreated: &slackChannelCreatedTriggerModel{
						ChannelNameContains: types.StringValue("incident"),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		scc := wf.Triggers[0].GetSlackChannelCreated()
		if scc == nil {
			t.Fatal("expected slack_channel_created trigger in proto")
		}
		if scc.GetChannelNameContains() != "incident" {
			t.Fatalf("ChannelNameContains = %q, want %q", scc.GetChannelNameContains(), "incident")
		}
	})

	t.Run("proto_to_model_empty_filter_is_null", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Triggers: []*v1.Trigger{
						{Trigger: &v1.Trigger_SlackChannelCreated{SlackChannelCreated: &v1.SlackChannelCreatedTrigger{}}},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		scc := model.Triggers[0].SlackChannelCreated
		if scc == nil {
			t.Fatal("expected slack_channel_created trigger in terraform model")
		}
		if !scc.ChannelNameContains.IsNull() {
			t.Fatalf("expected null channel_name_contains, got %v", scc.ChannelNameContains)
		}
	})
}

func TestSlackReactionAddedTriggerRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("triage reactions"),
			Triggers: []triggerModel{
				{
					SlackReactionAdded: &slackReactionAddedTriggerModel{
						Channel:                        types.StringValue("C0123456789"),
						EmojiName:                      types.StringValue("white_check_mark"),
						BlockUnauthenticatedSlackUsers: types.BoolValue(true),
						OnlyOwnerReactions:             types.BoolNull(),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		sra := wf.Triggers[0].GetSlackReactionAdded()
		if sra == nil {
			t.Fatal("expected slack_reaction_added trigger in proto")
		}
		if sra.GetChannel() != "C0123456789" {
			t.Fatalf("Channel = %q", sra.GetChannel())
		}
		if sra.GetEmojiName() != "white_check_mark" {
			t.Fatalf("EmojiName = %q", sra.GetEmojiName())
		}
		if !sra.GetBlockUnauthenticatedSlackUsers() {
			t.Fatal("expected BlockUnauthenticatedSlackUsers=true")
		}
		if sra.GetOnlyOwnerReactions() {
			t.Fatal("expected OnlyOwnerReactions=false when unset")
		}
	})

	t.Run("model_to_proto_rejects_non_canonical_emoji", func(t *testing.T) {
		for _, emoji := range []string{":thumbsup:", "ThumbsUp", "thumbsup::skin-tone-2", ""} {
			m := &platformWorkflowModel{
				Prompt: types.StringValue("triage reactions"),
				Triggers: []triggerModel{
					{
						SlackReactionAdded: &slackReactionAddedTriggerModel{
							Channel:   types.StringValue("C0123456789"),
							EmojiName: types.StringValue(emoji),
						},
						UserAllowlist: types.ListNull(types.StringType),
					},
				},
			}
			if _, err := modelToWorkflow(ctx, m); err == nil {
				t.Errorf("expected error for emoji_name %q", emoji)
			}
		}
	})

	t.Run("model_to_proto_requires_channel", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("triage reactions"),
			Triggers: []triggerModel{
				{
					SlackReactionAdded: &slackReactionAddedTriggerModel{
						Channel:   types.StringValue(" "),
						EmojiName: types.StringValue("eyes"),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}
		_, err := modelToWorkflow(ctx, m)
		if err == nil || !strings.Contains(err.Error(), "slack_reaction_added.channel is required") {
			t.Fatalf("expected channel required error, got %v", err)
		}
	})

	t.Run("proto_to_model_prefers_channels_and_nulls_false_bools", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Triggers: []*v1.Trigger{
						{Trigger: &v1.Trigger_SlackReactionAdded{SlackReactionAdded: &v1.SlackReactionAddedTrigger{
							Channel:            "C0123456789",
							Channels:           []string{"C0123456789"},
							EmojiName:          "eyes",
							OnlyOwnerReactions: true,
						}}},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		sra := model.Triggers[0].SlackReactionAdded
		if sra == nil {
			t.Fatal("expected slack_reaction_added trigger in terraform model")
		}
		if sra.Channel.ValueString() != "C0123456789" {
			t.Fatalf("Channel = %q", sra.Channel.ValueString())
		}
		if sra.EmojiName.ValueString() != "eyes" {
			t.Fatalf("EmojiName = %q", sra.EmojiName.ValueString())
		}
		if !sra.BlockUnauthenticatedSlackUsers.IsNull() {
			t.Fatal("expected null block_unauthenticated_slack_users when false")
		}
		if !sra.OnlyOwnerReactions.ValueBool() {
			t.Fatal("expected only_owner_reactions=true")
		}
	})
}

func TestSlackMentionAndAnyReactionTriggerRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("respond to mentions"),
			Triggers: []triggerModel{
				{
					SlackMention: &slackMentionTriggerModel{
						Channel:                        types.StringValue("C1"),
						BlockUnauthenticatedSlackUsers: types.BoolValue(true),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
				{
					SlackAnyReactionAdded: &slackAnyReactionAddedTriggerModel{
						Channel:            types.StringValue("C2"),
						OnlyOwnerReactions: types.BoolValue(true),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		mention := wf.Triggers[0].GetSlackMention()
		if mention == nil || mention.GetChannel() != "C1" || !mention.GetBlockUnauthenticatedSlackUsers() {
			t.Fatalf("unexpected slack_mention proto: %v", mention)
		}
		anyReaction := wf.Triggers[1].GetSlackAnyReactionAdded()
		if anyReaction == nil || anyReaction.GetChannel() != "C2" || !anyReaction.GetOnlyOwnerReactions() || anyReaction.GetBlockUnauthenticatedSlackUsers() {
			t.Fatalf("unexpected slack_any_reaction_added proto: %v", anyReaction)
		}
	})

	t.Run("proto_to_model", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Triggers: []*v1.Trigger{
						{Trigger: &v1.Trigger_SlackMention{SlackMention: &v1.SlackMentionTrigger{Channels: []string{"C1", "C9"}}}},
						{Trigger: &v1.Trigger_SlackAnyReactionAdded{SlackAnyReactionAdded: &v1.SlackAnyReactionAddedTrigger{
							Channel:                        "C2",
							BlockUnauthenticatedSlackUsers: true,
						}}},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		mention := model.Triggers[0].SlackMention
		if mention == nil || mention.Channel.ValueString() != "C1" || !mention.BlockUnauthenticatedSlackUsers.IsNull() {
			t.Fatalf("unexpected slack_mention model: %+v", mention)
		}
		anyReaction := model.Triggers[1].SlackAnyReactionAdded
		if anyReaction == nil || anyReaction.Channel.ValueString() != "C2" || !anyReaction.BlockUnauthenticatedSlackUsers.ValueBool() || !anyReaction.OnlyOwnerReactions.IsNull() {
			t.Fatalf("unexpected slack_any_reaction_added model: %+v", anyReaction)
		}
	})
}

func TestPagerDutyTriggerRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("handle incidents"),
			Triggers: []triggerModel{
				{
					PagerDuty: &pagerDutyTriggerModel{
						IncidentTriggered: &emptyEventModel{},
						ServiceIDs:        mustStringList(t, ctx, []string{"PABC123"}),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		pd := wf.Triggers[0].GetPagerduty()
		if pd == nil {
			t.Fatal("expected pagerduty trigger in proto")
		}
		if pd.GetIncidentTriggered() == nil {
			t.Fatal("expected incident_triggered event in proto")
		}
		if !reflect.DeepEqual(pd.GetServiceIds(), []string{"PABC123"}) {
			t.Fatalf("ServiceIds = %v", pd.GetServiceIds())
		}
	})

	t.Run("model_to_proto_requires_exactly_one_event", func(t *testing.T) {
		for name, pd := range map[string]*pagerDutyTriggerModel{
			"none": {ServiceIDs: types.ListNull(types.StringType)},
			"two":  {IncidentAny: &emptyEventModel{}, IncidentResolved: &emptyEventModel{}, ServiceIDs: types.ListNull(types.StringType)},
		} {
			m := &platformWorkflowModel{
				Prompt:   types.StringValue("handle incidents"),
				Triggers: []triggerModel{{PagerDuty: pd, UserAllowlist: types.ListNull(types.StringType)}},
			}
			if _, err := modelToWorkflow(ctx, m); err == nil {
				t.Errorf("%s: expected error", name)
			}
		}
	})

	t.Run("proto_to_model", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Triggers: []*v1.Trigger{
						{Trigger: &v1.Trigger_Pagerduty{Pagerduty: &v1.PagerDutyTrigger{
							Event: &v1.PagerDutyTrigger_IncidentAny{IncidentAny: &v1.PagerDutyIncidentAnyEvent{}},
						}}},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		pd := model.Triggers[0].PagerDuty
		if pd == nil {
			t.Fatal("expected pagerduty trigger in terraform model")
		}
		if pd.IncidentAny == nil || pd.IncidentTriggered != nil {
			t.Fatalf("unexpected pagerduty events: %+v", pd)
		}
		if !pd.ServiceIDs.IsNull() {
			t.Fatal("expected null service_ids when empty")
		}
	})
}

func TestSentryTriggerRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("triage sentry issues"),
			Triggers: []triggerModel{
				{
					Sentry: &sentryTriggerModel{
						IssueCreated: &emptyEventModel{},
						ProjectIDs:   mustStringList(t, ctx, []string{"123", "456"}),
					},
					UserAllowlist: types.ListNull(types.StringType),
				},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		sentry := wf.Triggers[0].GetSentry()
		if sentry == nil {
			t.Fatal("expected sentry trigger in proto")
		}
		if sentry.GetIssueCreated() == nil {
			t.Fatal("expected issue_created event in proto")
		}
		if !reflect.DeepEqual(sentry.GetProjectIds(), []string{"123", "456"}) {
			t.Fatalf("ProjectIds = %v", sentry.GetProjectIds())
		}
	})

	t.Run("model_to_proto_requires_exactly_one_event", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("triage sentry issues"),
			Triggers: []triggerModel{{
				Sentry:        &sentryTriggerModel{ProjectIDs: types.ListNull(types.StringType)},
				UserAllowlist: types.ListNull(types.StringType),
			}},
		}
		if _, err := modelToWorkflow(ctx, m); err == nil {
			t.Fatal("expected error when no sentry event is set")
		}
	})

	t.Run("proto_to_model", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Triggers: []*v1.Trigger{
						{Trigger: &v1.Trigger_Sentry{Sentry: &v1.SentryTrigger{
							Event:      &v1.SentryTrigger_IssueUnresolved{IssueUnresolved: &v1.SentryIssueUnresolvedEvent{}},
							ProjectIds: []string{"123"},
						}}},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		sentry := model.Triggers[0].Sentry
		if sentry == nil {
			t.Fatal("expected sentry trigger in terraform model")
		}
		if sentry.IssueUnresolved == nil || sentry.IssueAny != nil {
			t.Fatalf("unexpected sentry events: %+v", sentry)
		}
		var projectIDs []string
		if diags := sentry.ProjectIDs.ElementsAs(ctx, &projectIDs, false); diags.HasError() {
			t.Fatalf("failed to read project_ids: %v", diags)
		}
		if !reflect.DeepEqual(projectIDs, []string{"123"}) {
			t.Fatalf("project_ids = %v", projectIDs)
		}
	})
}

func TestEmptyActionsRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("review code"),
			Triggers: []triggerModel{
				{Webhook: &webhookTriggerModel{}, UserAllowlist: types.ListNull(types.StringType)},
			},
			Actions: []actionModel{
				{ManageCheckRun: &manageCheckRunActionModel{}},
				{ApprovePr: &approvePrActionModel{}},
				{ResolveReviewThreads: &resolveReviewThreadsActionModel{}},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		if len(wf.Actions) != 3 {
			t.Fatalf("expected 3 actions, got %d", len(wf.Actions))
		}
		if wf.Actions[0].GetManageCheckRun() == nil {
			t.Error("expected manage_check_run action")
		}
		if wf.Actions[1].GetApprovePr() == nil {
			t.Error("expected approve_pr action")
		}
		if wf.Actions[2].GetResolveReviewThreads() == nil {
			t.Error("expected resolve_review_threads action")
		}
	})

	t.Run("model_to_proto_rejects_multiple_action_types", func(t *testing.T) {
		_, err := actionModelToProto(&actionModel{
			ManageCheckRun:       &manageCheckRunActionModel{},
			ResolveReviewThreads: &resolveReviewThreadsActionModel{},
		})
		if err == nil || !strings.Contains(err.Error(), "exactly one of") {
			t.Fatalf("expected exactly-one error, got %v", err)
		}
	})

	t.Run("proto_to_model", func(t *testing.T) {
		input := &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Actions: []*v1.Action{
						{Action: &v1.Action_ManageCheckRun{ManageCheckRun: &v1.ManageCheckRunAction{}}},
						{Action: &v1.Action_ApprovePr{ApprovePr: &v1.ApprovePrAction{}}},
						{Action: &v1.Action_ResolveReviewThreads{ResolveReviewThreads: &v1.ResolveReviewThreadsAction{}}},
					},
				},
			},
		}

		model, err := protoToModel(ctx, input)
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		if len(model.Actions) != 3 {
			t.Fatalf("expected 3 actions, got %d", len(model.Actions))
		}
		if model.Actions[0].ManageCheckRun == nil || model.Actions[1].ApprovePr == nil || model.Actions[2].ResolveReviewThreads == nil {
			t.Fatalf("unexpected actions: %+v", model.Actions)
		}
	})
}

func TestMicrosoftTeamsActionRejectsRespondAndPostAsThread(t *testing.T) {
	_, err := actionModelToProto(&actionModel{
		MicrosoftTeams: &microsoftTeamsActionModel{
			TenantID:        types.StringValue("tenant"),
			TeamID:          types.StringValue("team"),
			ChannelIDs:      types.ListNull(types.StringType),
			RespondInThread: types.BoolValue(true),
			PostAsThread:    types.BoolValue(true),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "respond_in_thread and post_as_thread") {
		t.Fatalf("expected respond_in_thread/post_as_thread error, got %v", err)
	}
}

func TestMicrosoftTeamsChannelCreatedRequiresTeamIDs(t *testing.T) {
	ctx := context.Background()

	m := &platformWorkflowModel{
		Prompt: types.StringValue("welcome channels"),
		Triggers: []triggerModel{{
			MicrosoftTeamsChannelCreated: &microsoftTeamsChannelCreatedTriggerModel{
				TenantID: types.StringValue("tenant"),
				TeamIDs:  types.ListNull(types.StringType),
			},
			UserAllowlist: types.ListNull(types.StringType),
		}},
	}
	if _, err := modelToWorkflow(ctx, m); err == nil || !strings.Contains(err.Error(), "at least one team_id") {
		t.Fatalf("expected team_id required error, got %v", err)
	}

	r := &platformWorkflowResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	trigger := schemaResp.Schema.Attributes["trigger"].(schema.ListNestedAttribute)
	mtc := trigger.NestedObject.Attributes["microsoft_teams_channel_created"].(schema.SingleNestedAttribute)
	if !mtc.Attributes["team_ids"].(schema.ListAttribute).Required {
		t.Fatal("microsoft_teams_channel_created.team_ids should be Required")
	}
}

func TestSlackActionRespondInThreadIsDeprecated(t *testing.T) {
	r := &platformWorkflowResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, schemaResp)
	action := schemaResp.Schema.Attributes["action"].(schema.ListNestedAttribute)
	slack := action.NestedObject.Attributes["slack"].(schema.SingleNestedAttribute)
	attr := slack.Attributes["respond_in_thread"].(schema.BoolAttribute)
	if attr.DeprecationMessage == "" {
		t.Fatal("slack.respond_in_thread should carry a DeprecationMessage")
	}
	if !attr.Optional || !attr.Computed {
		t.Fatal("slack.respond_in_thread should stay Optional+Computed so existing configs keep working")
	}
}

func TestDisabledDefaultToolsRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt:               types.StringValue("review code"),
			DisabledDefaultTools: mustStringList(t, ctx, []string{"open_git_pr"}),
			Triggers: []triggerModel{
				{Webhook: &webhookTriggerModel{}, UserAllowlist: types.ListNull(types.StringType)},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		want := []v1.AutomationDefaultTool{v1.AutomationDefaultTool_AUTOMATION_DEFAULT_TOOL_OPEN_GIT_PR}
		if !reflect.DeepEqual(wf.GetDisabledDefaultTools(), want) {
			t.Fatalf("DisabledDefaultTools = %v, want %v", wf.GetDisabledDefaultTools(), want)
		}
	})

	t.Run("model_to_proto_rejects_unknown_tool", func(t *testing.T) {
		for _, entry := range []string{"nope", "OPEN_GIT_PR", " open_git_pr"} {
			m := &platformWorkflowModel{
				Prompt:               types.StringValue("review code"),
				DisabledDefaultTools: mustStringList(t, ctx, []string{entry}),
				Triggers: []triggerModel{
					{Webhook: &webhookTriggerModel{}, UserAllowlist: types.ListNull(types.StringType)},
				},
			}
			if _, err := modelToWorkflow(ctx, m); err == nil {
				t.Errorf("expected error for disabled_default_tools entry %q", entry)
			}
		}
		m := &platformWorkflowModel{
			Prompt:               types.StringValue("review code"),
			DisabledDefaultTools: mustStringList(t, ctx, []string{"nope"}),
			Triggers: []triggerModel{
				{Webhook: &webhookTriggerModel{}, UserAllowlist: types.ListNull(types.StringType)},
			},
		}
		if _, err := modelToWorkflow(ctx, m); err == nil {
			t.Fatal("expected error for unknown disabled_default_tools entry")
		}
	})

	t.Run("proto_to_model", func(t *testing.T) {
		model, err := protoToModel(ctx, &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					DisabledDefaultTools: []v1.AutomationDefaultTool{v1.AutomationDefaultTool_AUTOMATION_DEFAULT_TOOL_OPEN_GIT_PR},
				},
			},
		})
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		var tools []string
		if diags := model.DisabledDefaultTools.ElementsAs(ctx, &tools, false); diags.HasError() {
			t.Fatalf("failed to read disabled_default_tools: %v", diags)
		}
		if !reflect.DeepEqual(tools, []string{"open_git_pr"}) {
			t.Fatalf("disabled_default_tools = %v", tools)
		}

		empty, err := protoToModel(ctx, &v1.AutomationWithOwner{Workflow: &v1.Automation{Workflow: &v1.Workflow{}}})
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		if !empty.DisabledDefaultTools.IsNull() {
			t.Fatal("expected null disabled_default_tools when the server returns none")
		}
	})
}

func TestDescriptionRoundTrip(t *testing.T) {
	ctx := context.Background()

	described := "Reviews PRs"
	model, err := protoToModel(ctx, &v1.AutomationWithOwner{
		Workflow: &v1.Automation{Description: &described, Workflow: &v1.Workflow{}},
	})
	if err != nil {
		t.Fatalf("protoToModel() error: %v", err)
	}
	if model.Description.ValueString() != described {
		t.Fatalf("Description = %q", model.Description.ValueString())
	}

	empty := ""
	model, err = protoToModel(ctx, &v1.AutomationWithOwner{
		Workflow: &v1.Automation{Description: &empty, Workflow: &v1.Workflow{}},
	})
	if err != nil {
		t.Fatalf("protoToModel() error: %v", err)
	}
	if !model.Description.IsNull() {
		t.Fatal("expected null description when the server returns an empty string")
	}

	cases := []struct {
		name  string
		plan  types.String
		state types.String
		want  bool
	}{
		{"unchanged", types.StringValue("a"), types.StringValue("a"), false},
		{"changed", types.StringValue("b"), types.StringValue("a"), true},
		{"added", types.StringValue("a"), types.StringNull(), true},
		{"removed", types.StringNull(), types.StringValue("a"), true},
		{"both_unset", types.StringNull(), types.StringNull(), false},
		{"unknown_plan", types.StringUnknown(), types.StringValue("a"), false},
	}
	for _, tc := range cases {
		if got := shouldUpdateDescription(tc.plan, tc.state); got != tc.want {
			t.Errorf("%s: shouldUpdateDescription = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTeamIDIsCarriedOverNotReadBack(t *testing.T) {
	ctx := context.Background()

	serverTeam := int32(42)
	state, err := protoToModel(ctx, &v1.AutomationWithOwner{
		TeamId:   &serverTeam,
		Workflow: &v1.Automation{Workflow: &v1.Workflow{}},
	})
	if err != nil {
		t.Fatalf("protoToModel() error: %v", err)
	}
	if !state.TeamID.IsNull() {
		t.Fatal("protoToModel should not populate team_id from the server response")
	}

	plan := platformWorkflowModel{TeamID: types.Int64Value(7)}
	preserveConfiguredValues(ctx, &state, plan)
	if state.TeamID.ValueInt64() != 7 {
		t.Fatalf("team_id = %v, want configured 7", state.TeamID)
	}

	if got := optionalTeamID(types.Int64Null()); got != nil {
		t.Fatalf("optionalTeamID(null) = %v, want nil", *got)
	}
	if got := optionalTeamID(types.Int64Value(7)); got == nil || *got != 7 {
		t.Fatalf("optionalTeamID(7) = %v", got)
	}

	if got := dataSourceTeamID(types.Int64Null(), &v1.AutomationWithOwner{TeamId: &serverTeam}); got.ValueInt64() != 42 {
		t.Fatalf("dataSourceTeamID(unset) = %v, want server 42", got)
	}
	if got := dataSourceTeamID(types.Int64Value(7), &v1.AutomationWithOwner{TeamId: &serverTeam}); got.ValueInt64() != 7 {
		t.Fatalf("dataSourceTeamID(configured) = %v, want 7", got)
	}
	if got := dataSourceTeamID(types.Int64Null(), &v1.AutomationWithOwner{}); !got.IsNull() {
		t.Fatalf("dataSourceTeamID(no team) = %v, want null", got)
	}
}

func TestAutomationFromGetResponseRestrictedSummary(t *testing.T) {
	_, err := automationFromGetResponse(&v1.GetAutomationResponse{
		Result: &v1.GetAutomationResponse_RestrictedSummary{
			RestrictedSummary: &v1.RestrictedAutomationSummary{
				AutomationId: "abc",
				Name:         "Private automation",
				OwnerName:    "Jane",
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for restricted summary")
	}
	for _, want := range []string{"abc", "Private automation", "Jane", "restricted summary"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err.Error(), want)
		}
	}

	withOwner, err := automationFromGetResponse(&v1.GetAutomationResponse{
		Result: &v1.GetAutomationResponse_Workflow{
			Workflow: &v1.AutomationWithOwner{Workflow: &v1.Automation{AutomationId: "abc"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if withOwner.GetWorkflow().GetAutomationId() != "abc" {
		t.Fatal("expected workflow variant to be returned unchanged")
	}
}

// TestDataSourceSchemaMirrorsResourceSchema guards the six-location sync rule:
// every trigger and action block the resource exposes must exist in the data
// source with the same nested attribute names.
func TestDataSourceSchemaMirrorsResourceSchema(t *testing.T) {
	ctx := context.Background()

	r := &platformWorkflowResource{}
	rResp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, rResp)

	d := &platformWorkflowDataSource{}
	dResp := &datasource.SchemaResponse{}
	d.Schema(ctx, datasource.SchemaRequest{}, dResp)

	rAttrs := rResp.Schema.Attributes
	dAttrs := dResp.Schema.Attributes
	for name := range rAttrs {
		if _, ok := dAttrs[name]; !ok {
			t.Errorf("data source is missing top-level attribute %q", name)
		}
	}
	for name := range dAttrs {
		if _, ok := rAttrs[name]; !ok {
			t.Errorf("resource is missing top-level attribute %q", name)
		}
	}

	for _, block := range []string{"trigger", "action"} {
		rNested := rAttrs[block].(schema.ListNestedAttribute).NestedObject.Attributes
		dNested := dAttrs[block].(datasourceschema.ListNestedAttribute).NestedObject.Attributes
		for name, rAttr := range rNested {
			dAttr, ok := dNested[name]
			if !ok {
				t.Errorf("data source %s is missing %q", block, name)
				continue
			}
			rSingle, rIsSingle := rAttr.(schema.SingleNestedAttribute)
			dSingle, dIsSingle := dAttr.(datasourceschema.SingleNestedAttribute)
			if rIsSingle != dIsSingle {
				t.Errorf("%s.%s: nested kind differs between resource and data source", block, name)
				continue
			}
			if !rIsSingle {
				continue
			}
			for field := range rSingle.Attributes {
				if _, ok := dSingle.Attributes[field]; !ok {
					t.Errorf("data source %s.%s is missing %q", block, name, field)
				}
			}
			for field := range dSingle.Attributes {
				if _, ok := rSingle.Attributes[field]; !ok {
					t.Errorf("resource %s.%s is missing %q", block, name, field)
				}
			}
		}
		for name := range dNested {
			if _, ok := rNested[name]; !ok {
				t.Errorf("resource %s is missing %q", block, name)
			}
		}
	}
}

func modelSelectionParams(pairs ...string) []modelSelectionParameterModel {
	var out []modelSelectionParameterModel
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, modelSelectionParameterModel{
			ID:    types.StringValue(pairs[i]),
			Value: types.StringValue(pairs[i+1]),
		})
	}
	return out
}

func TestModelSelectionRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("model_to_proto_auto_cost", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("review code"),
			// Carried over from state via the plan modifier; must not be sent
			// alongside the selection.
			Model: types.StringValue("auto-smart"),
			ModelSelection: &modelSelectionModel{
				ModelID:    types.StringValue("auto-smart"),
				Parameters: modelSelectionParams("optimize_for", "cost"),
				MaxMode:    types.BoolNull(),
			},
			Triggers: []triggerModel{
				{Webhook: &webhookTriggerModel{}, UserAllowlist: types.ListNull(types.StringType)},
			},
		}

		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		if wf.Model != nil {
			t.Fatalf("model slug should not be sent with a selection, got %q", wf.GetModel())
		}
		sel := wf.GetModelSelection()
		if sel == nil {
			t.Fatal("expected model_selection in proto")
		}
		if sel.GetModelId() != "auto-smart" {
			t.Fatalf("ModelId = %q", sel.GetModelId())
		}
		if len(sel.GetParameters()) != 1 || sel.GetParameters()[0].GetId() != "optimize_for" || sel.GetParameters()[0].GetValue() != "cost" {
			t.Fatalf("Parameters = %v", sel.GetParameters())
		}
		if sel.MaxMode != nil {
			t.Fatal("max_mode should be omitted when unset so the server applies its default")
		}
	})

	t.Run("model_to_proto_without_selection_sends_slug", func(t *testing.T) {
		m := &platformWorkflowModel{
			Prompt: types.StringValue("review code"),
			Model:  types.StringValue("gpt-5.5"),
			Triggers: []triggerModel{
				{Webhook: &webhookTriggerModel{}, UserAllowlist: types.ListNull(types.StringType)},
			},
		}
		wf, err := modelToWorkflow(ctx, m)
		if err != nil {
			t.Fatalf("modelToWorkflow() error: %v", err)
		}
		if wf.GetModel() != "gpt-5.5" || wf.GetModelSelection() != nil {
			t.Fatalf("unexpected model fields: model=%q selection=%v", wf.GetModel(), wf.GetModelSelection())
		}
	})

	t.Run("model_to_proto_validation", func(t *testing.T) {
		cases := map[string]*modelSelectionModel{
			"empty_model_id":      {ModelID: types.StringValue(" ")},
			"incomplete_param":    {ModelID: types.StringValue("auto-smart"), Parameters: modelSelectionParams("optimize_for", "")},
			"duplicate_param":     {ModelID: types.StringValue("auto-smart"), Parameters: modelSelectionParams("optimize_for", "cost", "optimize_for", "balanced")},
			"max_mode_false":      {ModelID: types.StringValue("auto-smart"), MaxMode: types.BoolValue(false)},
			"max_mode_false_only": {ModelID: types.StringValue("gpt-5.5"), MaxMode: types.BoolValue(false)},
		}
		for name, sel := range cases {
			if _, err := modelSelectionToProto(sel); err == nil {
				t.Errorf("%s: expected error", name)
			}
		}

		sel, err := modelSelectionToProto(&modelSelectionModel{
			ModelID: types.StringValue(" auto-smart "),
			Parameters: []modelSelectionParameterModel{
				{ID: types.StringValue(" optimize_for "), Value: types.StringValue(" balanced ")},
			},
			MaxMode: types.BoolValue(true),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sel.GetModelId() != "auto-smart" || sel.GetParameters()[0].GetId() != "optimize_for" || sel.GetParameters()[0].GetValue() != "balanced" || sel.MaxMode == nil || !sel.GetMaxMode() {
			t.Fatalf("unexpected trimmed selection: %v", sel)
		}
	})

	t.Run("proto_to_model_set", func(t *testing.T) {
		maxMode := true
		model := "auto-smart"
		out, err := protoToModel(ctx, &v1.AutomationWithOwner{
			Workflow: &v1.Automation{
				Workflow: &v1.Workflow{
					Model: &model,
					ModelSelection: &v1.AutomationModelSelection{
						ModelId: "auto-smart",
						Parameters: []*v1.AutomationModelSelection_ParameterValue{
							{Id: "optimize_for", Value: "balanced"},
						},
						MaxMode: &maxMode,
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("protoToModel() error: %v", err)
		}
		if out.Model.ValueString() != "auto-smart" {
			t.Fatalf("model = %q", out.Model.ValueString())
		}
		sel := out.ModelSelection
		if sel == nil {
			t.Fatal("expected model_selection in terraform model")
		}
		if sel.ModelID.ValueString() != "auto-smart" {
			t.Fatalf("model_id = %q", sel.ModelID.ValueString())
		}
		if len(sel.Parameters) != 1 || sel.Parameters[0].ID.ValueString() != "optimize_for" || sel.Parameters[0].Value.ValueString() != "balanced" {
			t.Fatalf("parameters = %+v", sel.Parameters)
		}
		if !sel.MaxMode.ValueBool() {
			t.Fatal("expected max_mode=true")
		}
	})

	t.Run("proto_to_model_null_when_absent_or_blank", func(t *testing.T) {
		model := "gpt-5.5"
		for name, wf := range map[string]*v1.Workflow{
			"absent": {Model: &model},
			"blank":  {Model: &model, ModelSelection: &v1.AutomationModelSelection{ModelId: " "}},
		} {
			out, err := protoToModel(ctx, &v1.AutomationWithOwner{Workflow: &v1.Automation{Workflow: wf}})
			if err != nil {
				t.Fatalf("%s: protoToModel() error: %v", name, err)
			}
			if out.ModelSelection != nil {
				t.Fatalf("%s: expected null model_selection, got %+v", name, out.ModelSelection)
			}
			if out.Model.ValueString() != "gpt-5.5" {
				t.Fatalf("%s: model = %q", name, out.Model.ValueString())
			}
		}
	})
}

func TestPreserveEquivalentModelSelection(t *testing.T) {
	t.Run("keeps_configured_order_and_whitespace", func(t *testing.T) {
		state := platformWorkflowModel{ModelSelection: &modelSelectionModel{
			ModelID:    types.StringValue("auto-smart"),
			Parameters: modelSelectionParams("a", "1", "optimize_for", "cost"),
			MaxMode:    types.BoolValue(true),
		}}
		reference := platformWorkflowModel{ModelSelection: &modelSelectionModel{
			ModelID:    types.StringValue(" auto-smart"),
			Parameters: modelSelectionParams("optimize_for", "cost ", "a", "1"),
			MaxMode:    types.BoolNull(),
		}}
		preserveEquivalentModelSelection(&state, reference)
		if state.ModelSelection.ModelID.ValueString() != " auto-smart" {
			t.Fatalf("model_id = %q, want configured spelling", state.ModelSelection.ModelID.ValueString())
		}
		if !reflect.DeepEqual(state.ModelSelection.Parameters, reference.ModelSelection.Parameters) {
			t.Fatalf("parameters = %+v, want configured order", state.ModelSelection.Parameters)
		}
		if !state.ModelSelection.MaxMode.ValueBool() {
			t.Fatal("max_mode must keep the server value")
		}
	})

	t.Run("keeps_server_values_when_different", func(t *testing.T) {
		state := platformWorkflowModel{ModelSelection: &modelSelectionModel{
			ModelID:    types.StringValue("auto-smart"),
			Parameters: modelSelectionParams("optimize_for", "balanced"),
		}}
		reference := platformWorkflowModel{ModelSelection: &modelSelectionModel{
			ModelID:    types.StringValue("auto-smart"),
			Parameters: modelSelectionParams("optimize_for", "cost"),
		}}
		preserveEquivalentModelSelection(&state, reference)
		if state.ModelSelection.Parameters[0].Value.ValueString() != "balanced" {
			t.Fatal("differing parameters must not be overwritten with the configured ones")
		}

		// Server-populated defaults when the config omitted parameters.
		state = platformWorkflowModel{ModelSelection: &modelSelectionModel{
			ModelID:    types.StringValue("auto-smart"),
			Parameters: modelSelectionParams("optimize_for", "balanced"),
		}}
		reference = platformWorkflowModel{ModelSelection: &modelSelectionModel{ModelID: types.StringValue("auto-smart")}}
		preserveEquivalentModelSelection(&state, reference)
		if len(state.ModelSelection.Parameters) != 1 {
			t.Fatal("server default parameters must be kept when the config omitted them")
		}
	})

	t.Run("noop_without_selection", func(t *testing.T) {
		state := platformWorkflowModel{}
		preserveEquivalentModelSelection(&state, platformWorkflowModel{ModelSelection: &modelSelectionModel{ModelID: types.StringValue("x")}})
		if state.ModelSelection != nil {
			t.Fatal("state without a selection must stay null")
		}
	})
}

func TestModelSelectionSchema(t *testing.T) {
	r := &platformWorkflowResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, schemaResp)

	sel, ok := schemaResp.Schema.Attributes["model_selection"].(schema.SingleNestedAttribute)
	if !ok || !sel.Optional || sel.Computed {
		t.Fatalf("model_selection should be an Optional, non-Computed SingleNestedAttribute, got %#v", schemaResp.Schema.Attributes["model_selection"])
	}
	if modelID := sel.Attributes["model_id"].(schema.StringAttribute); !modelID.Required {
		t.Fatal("model_selection.model_id should be Required")
	}
	params := sel.Attributes["parameters"].(schema.ListNestedAttribute)
	if !params.Optional || !params.Computed || len(params.PlanModifiers) == 0 {
		t.Fatal("model_selection.parameters should be Optional+Computed with a plan modifier (server fills default variant parameters)")
	}
	maxMode := sel.Attributes["max_mode"].(schema.BoolAttribute)
	if !maxMode.Optional || !maxMode.Computed || len(maxMode.PlanModifiers) == 0 {
		t.Fatal("model_selection.max_mode should be Optional+Computed with UseStateForUnknown (server defaults it to true)")
	}
	model := schemaResp.Schema.Attributes["model"].(schema.StringAttribute)
	if !model.Optional || !model.Computed || len(model.PlanModifiers) != 1 {
		t.Fatal("model should stay Optional+Computed with a single plan modifier")
	}
	if _, ok := model.PlanModifiers[0].(modelUseStateUnlessSelectionChanged); !ok {
		t.Fatalf("model plan modifier = %T, want modelUseStateUnlessSelectionChanged", model.PlanModifiers[0])
	}
}

// planStateForModelSelection builds tfsdk.Plan/State values holding just the
// attributes the model plan modifiers read.
func planStateForModelSelection(t *testing.T, model types.String, selection *modelSelectionModel, selectionUnknownParams bool) (tfsdk.Plan, tfsdk.State) {
	t.Helper()
	ctx := context.Background()

	r := &platformWorkflowResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, schemaResp)

	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	// Zero-value lists/maps carry no element type, so give them typed nulls.
	m := &platformWorkflowModel{
		Model:                model,
		ModelSelection:       selection,
		DisabledDefaultTools: types.ListNull(types.StringType),
	}
	if diags := plan.Set(ctx, m); diags.HasError() {
		t.Fatalf("set plan: %v", diags)
	}
	if selection != nil && selectionUnknownParams {
		paramType := types.ObjectType{AttrTypes: map[string]attr.Type{"id": types.StringType, "value": types.StringType}}
		if diags := plan.SetAttribute(ctx, path.Root("model_selection").AtName("parameters"), types.ListUnknown(paramType)); diags.HasError() {
			t.Fatalf("set unknown parameters: %v", diags)
		}
	}
	return plan, tfsdk.State{Schema: schemaResp.Schema, Raw: plan.Raw}
}

func TestModelPlanModifiersFollowSelectionChanges(t *testing.T) {
	ctx := context.Background()
	balanced := &modelSelectionModel{ModelID: types.StringValue("auto-smart"), Parameters: modelSelectionParams("optimize_for", "balanced"), MaxMode: types.BoolValue(true)}
	cost := &modelSelectionModel{ModelID: types.StringValue("auto-smart"), Parameters: modelSelectionParams("optimize_for", "cost"), MaxMode: types.BoolValue(true)}
	other := &modelSelectionModel{ModelID: types.StringValue("gpt-5.5"), MaxMode: types.BoolValue(true)}

	_, state := planStateForModelSelection(t, types.StringValue("auto-smart"), balanced, false)

	run := func(t *testing.T, plan tfsdk.Plan) (types.String, types.List) {
		t.Helper()
		strReq := planmodifier.StringRequest{Path: path.Root("model"), Plan: plan, State: state, PlanValue: types.StringUnknown(), StateValue: types.StringValue("auto-smart"), ConfigValue: types.StringNull()}
		strResp := &planmodifier.StringResponse{PlanValue: strReq.PlanValue}
		modelUseStateUnlessSelectionChanged{}.PlanModifyString(ctx, strReq, strResp)

		var stateSel types.Object
		state.GetAttribute(ctx, path.Root("model_selection"), &stateSel)
		stateParams := stateSel.Attributes()["parameters"].(types.List)
		listReq := planmodifier.ListRequest{Path: path.Root("model_selection").AtName("parameters"), Plan: plan, State: state, PlanValue: types.ListUnknown(stateParams.ElementType(ctx)), StateValue: stateParams, ConfigValue: types.ListNull(stateParams.ElementType(ctx))}
		listResp := &planmodifier.ListResponse{PlanValue: listReq.PlanValue}
		modelSelectionParametersUseStateUnlessModelChanged{}.PlanModifyList(ctx, listReq, listResp)
		return strResp.PlanValue, listResp.PlanValue
	}

	t.Run("unchanged_selection_keeps_state", func(t *testing.T) {
		plan, _ := planStateForModelSelection(t, types.StringUnknown(), balanced, false)
		model, params := run(t, plan)
		if model.IsUnknown() || model.ValueString() != "auto-smart" {
			t.Fatalf("model = %v, want state value", model)
		}
		if params.IsUnknown() {
			t.Fatal("parameters should be carried from state")
		}
	})

	t.Run("unconfigured_parameters_keep_state", func(t *testing.T) {
		plan, _ := planStateForModelSelection(t, types.StringUnknown(), balanced, true)
		model, params := run(t, plan)
		if model.IsUnknown() || params.IsUnknown() {
			t.Fatalf("unknown parameters with the same model_id must not reset model (%v) or parameters (%v)", model, params)
		}
	})

	t.Run("changed_parameters_reset_model", func(t *testing.T) {
		plan, _ := planStateForModelSelection(t, types.StringUnknown(), cost, false)
		model, _ := run(t, plan)
		if !model.IsUnknown() {
			t.Fatalf("model = %v, want unknown after a parameter change", model)
		}
	})

	t.Run("changed_model_id_resets_both", func(t *testing.T) {
		plan, _ := planStateForModelSelection(t, types.StringUnknown(), other, true)
		model, params := run(t, plan)
		if !model.IsUnknown() || !params.IsUnknown() {
			t.Fatalf("model (%v) and parameters (%v) must be unknown after a model_id change", model, params)
		}
	})

	t.Run("removed_selection_resets_model", func(t *testing.T) {
		plan, _ := planStateForModelSelection(t, types.StringUnknown(), nil, false)
		model, _ := run(t, plan)
		if !model.IsUnknown() {
			t.Fatalf("model = %v, want unknown after removing model_selection", model)
		}
	})
}

func TestValidateConfigRejectsModelWithModelSelection(t *testing.T) {
	ctx := context.Background()
	r := &platformWorkflowResource{}

	check := func(t *testing.T, model types.String, selection *modelSelectionModel, wantErr bool) {
		t.Helper()
		plan, _ := planStateForModelSelection(t, model, selection, false)
		resp := &resource.ValidateConfigResponse{}
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: plan.Schema, Raw: plan.Raw}}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Fatalf("model=%v selection=%v: HasError=%v, want %v (%v)", model, selection != nil, resp.Diagnostics.HasError(), wantErr, resp.Diagnostics)
		}
	}

	sel := &modelSelectionModel{ModelID: types.StringValue("auto-smart"), MaxMode: types.BoolNull()}
	check(t, types.StringValue("auto-smart"), sel, true)
	check(t, types.StringNull(), sel, false)
	// An unknown slug (e.g. from a variable) may still resolve to null.
	check(t, types.StringUnknown(), sel, false)
	check(t, types.StringValue("gpt-5.5"), nil, false)
	check(t, types.StringNull(), nil, false)
}

func TestNonBlankListEntriesRejected(t *testing.T) {
	ctx := context.Background()
	blank := mustStringList(t, ctx, []string{"ok", " "})

	cases := map[string]triggerModel{
		"microsoft_teams_channel_created.team_ids": {MicrosoftTeamsChannelCreated: &microsoftTeamsChannelCreatedTriggerModel{
			TenantID: types.StringValue("tenant"), TeamIDs: blank,
		}},
		"pagerduty.service_ids": {PagerDuty: &pagerDutyTriggerModel{IncidentAny: &emptyEventModel{}, ServiceIDs: blank}},
		"sentry.project_ids":    {Sentry: &sentryTriggerModel{IssueAny: &emptyEventModel{}, ProjectIDs: blank}},
	}
	for field, trigger := range cases {
		trigger.UserAllowlist = types.ListNull(types.StringType)
		_, err := triggerModelToProto(ctx, &trigger)
		if err == nil || !strings.Contains(err.Error(), field+"[1] must not be empty") {
			t.Errorf("%s: expected blank-entry error, got %v", field, err)
		}
	}
}

func TestPreserveEmptyDescription(t *testing.T) {
	state := platformWorkflowModel{Description: types.StringNull()}
	preserveEmptyDescription(&state, platformWorkflowModel{Description: types.StringValue("")})
	if state.Description.IsNull() || state.Description.ValueString() != "" {
		t.Fatalf("description = %v, want configured empty string", state.Description)
	}

	state = platformWorkflowModel{Description: types.StringNull()}
	preserveEmptyDescription(&state, platformWorkflowModel{Description: types.StringNull()})
	if !state.Description.IsNull() {
		t.Fatal("unset description must stay null")
	}

	state = platformWorkflowModel{Description: types.StringValue("server")}
	preserveEmptyDescription(&state, platformWorkflowModel{Description: types.StringValue("")})
	if state.Description.ValueString() != "server" {
		t.Fatal("a server-provided description must not be overwritten")
	}
}
