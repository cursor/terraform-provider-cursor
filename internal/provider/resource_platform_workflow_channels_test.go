package provider

import (
	"context"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	v1 "github.com/cursor/terraform-provider-cursor/internal/proto/v1"
	"github.com/cursor/terraform-provider-cursor/internal/proto/v1/v1connect"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/proto"
)

func TestSlackMultiChannelTriggerRoundTrip(t *testing.T) {
	ctx := context.Background()
	channels := []string{"C001", "C002", "C003", "C004", "C005"}
	for _, topLevel := range []*bool{nil, proto.Bool(false), proto.Bool(true)} {
		input := &v1.Trigger{Trigger: &v1.Trigger_SlackTrigger{SlackTrigger: &v1.SlackTrigger{
			Channel: channels[0], Channels: channels, TopLevelOnly: topLevel,
		}}}
		m, err := protoTriggerToModel(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		output, err := triggerModelToProto(ctx, &m)
		if err != nil || !proto.Equal(input, output) {
			t.Fatalf("top_level_only=%v round trip: %v\ninput=%v\noutput=%v", topLevel, err, input, output)
		}
	}
	for name, input := range map[string]*v1.Trigger{
		"reaction":     {Trigger: &v1.Trigger_SlackReactionAdded{SlackReactionAdded: &v1.SlackReactionAddedTrigger{Channel: channels[0], Channels: channels, EmojiName: "jira", OnlyOwnerReactions: true}}},
		"mention":      {Trigger: &v1.Trigger_SlackMention{SlackMention: &v1.SlackMentionTrigger{Channel: channels[0], Channels: channels, BlockUnauthenticatedSlackUsers: true}}},
		"any_reaction": {Trigger: &v1.Trigger_SlackAnyReactionAdded{SlackAnyReactionAdded: &v1.SlackAnyReactionAddedTrigger{Channel: channels[0], Channels: channels}}},
	} {
		t.Run(name, func(t *testing.T) {
			m, err := protoTriggerToModel(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			output, err := triggerModelToProto(ctx, &m)
			if err != nil || !proto.Equal(input, output) {
				t.Fatalf("round trip: %v\ninput=%v\noutput=%v", err, input, output)
			}
		})
	}
}

func TestSlackMultiChannelActionRoundTrip(t *testing.T) {
	input := &v1.Action{Action: &v1.Action_Slack{Slack: &v1.SlackAction{
		Channel: "C001", Channels: []string{"C001", "C002"}, RespondInThread: true,
	}}}
	m := protoActionToModel(input)
	output, err := actionModelToProto(&m)
	if err != nil || !proto.Equal(input, output) {
		t.Fatalf("round trip: %v\ninput=%v\noutput=%v", err, input, output)
	}
	if output.GetSlack().GetGeneralized() {
		t.Fatal("multi-channel allowlist must not enable generalized Slack access")
	}
}

func TestSlackLegacyChannelCompatibility(t *testing.T) {
	ctx := context.Background()
	for _, channels := range [][]string{nil, {"C001", "C002"}} {
		for name, input := range map[string]*v1.Trigger{
			"message":      {Trigger: &v1.Trigger_SlackTrigger{SlackTrigger: &v1.SlackTrigger{Channel: "C999", Channels: channels}}},
			"reaction":     {Trigger: &v1.Trigger_SlackReactionAdded{SlackReactionAdded: &v1.SlackReactionAddedTrigger{Channel: "C999", Channels: channels, EmojiName: "eyes"}}},
			"mention":      {Trigger: &v1.Trigger_SlackMention{SlackMention: &v1.SlackMentionTrigger{Channel: "C999", Channels: channels}}},
			"any_reaction": {Trigger: &v1.Trigger_SlackAnyReactionAdded{SlackAnyReactionAdded: &v1.SlackAnyReactionAddedTrigger{Channel: "C999", Channels: channels}}},
		} {
			t.Run(name+"/"+strings.Join(channels, ","), func(t *testing.T) {
				m, err := protoTriggerToModel(ctx, input)
				if err != nil {
					t.Fatal(err)
				}
				output, err := triggerModelToProto(ctx, &m)
				if err != nil {
					t.Fatal(err)
				}
				// Legacy API responses without a repeated list are normalized
				// to the equivalent single-channel list in Terraform state.
				want := proto.Clone(input).(*v1.Trigger)
				if channels == nil {
					switch trigger := want.Trigger.(type) {
					case *v1.Trigger_SlackTrigger:
						trigger.SlackTrigger.Channels = []string{"C999"}
					case *v1.Trigger_SlackReactionAdded:
						trigger.SlackReactionAdded.Channels = []string{"C999"}
					case *v1.Trigger_SlackMention:
						trigger.SlackMention.Channels = []string{"C999"}
					case *v1.Trigger_SlackAnyReactionAdded:
						trigger.SlackAnyReactionAdded.Channels = []string{"C999"}
					}
				}
				if !proto.Equal(want, output) {
					t.Fatalf("legacy scalar or routing changed: want=%v got=%v", want, output)
				}
			})
		}
		input := &v1.Action{Action: &v1.Action_Slack{Slack: &v1.SlackAction{Channel: "C999", Channels: channels}}}
		m := protoActionToModel(input)
		output, err := actionModelToProto(&m)
		want := proto.Clone(input).(*v1.Action)
		if channels == nil {
			want.GetSlack().Channels = []string{"C999"}
		}
		if err != nil || !proto.Equal(want, output) {
			t.Fatalf("legacy action changed: want=%v got=%v err=%v", want, output, err)
		}
	}
	if got := firstSlackChannel("", []string{"C001", "C002"}); got != "C001" {
		t.Fatalf("channels-only response should populate legacy scalar: %q", got)
	}
}

func TestSlackChannelSelection(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		channel     types.String
		channels    []string
		wantChannel string
		wantError   string
	}{
		{"legacy", types.StringValue("C001"), nil, "C001", ""},
		{"list_only", types.StringNull(), []string{"C001", "C002"}, "C001", ""},
		{"matching_both", types.StringValue("C001"), []string{"C001", "C002"}, "C001", ""},
		{"independent_both", types.StringValue("C999"), []string{"C001", "C002"}, "C999", ""},
		{"missing", types.StringNull(), nil, "", "is required"},
		{"empty_list", types.StringValue("C001"), []string{}, "", "must not be empty"},
		{"blank", types.StringNull(), []string{" "}, "", "must not be empty"},
		{"duplicate", types.StringNull(), []string{"C001", "C001"}, "", "duplicates"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := types.ListNull(types.StringType)
			if tc.channels != nil {
				list = mustStringList(t, ctx, tc.channels)
			}
			channel, channels, err := slackChannelSelection(ctx, tc.channel, list, "slack", true)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error=%v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || channel != tc.wantChannel || !reflect.DeepEqual(channels, tc.channels) {
				t.Fatalf("got %q %v %v", channel, channels, err)
			}
		})
	}
	// Preserve the scalar even when the repeated routing list differs.
	m, err := protoTriggerToModel(ctx, &v1.Trigger{Trigger: &v1.Trigger_SlackTrigger{SlackTrigger: &v1.SlackTrigger{Channel: "C999", Channels: []string{"C001", "C002"}}}})
	if err != nil || m.Slack.Channel.ValueString() != "C999" {
		t.Fatalf("legacy channel must not be overwritten on read: %v %v", m, err)
	}
}

func TestGitLabelTriggerRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, event := range []*v1.GitLabelEvent{
		{Repos: []string{"example/repo"}, LabelName: "cursor", OnAdded: true, PullRequests: true},
		{Repos: []string{"example/repo", "example/other"}, OnRemoved: true, Issues: true},
		{Repos: []string{"example/repo"}, OnAdded: true, OnRemoved: true, PullRequests: true, Issues: true},
	} {
		input := &v1.Trigger{Trigger: &v1.Trigger_Git{Git: &v1.GitTrigger{Event: &v1.GitTrigger_Label{Label: event}, UserAllowlist: []string{"reviewer"}}}}
		m, err := protoTriggerToModel(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		output, err := triggerModelToProto(ctx, &m)
		if err != nil || !proto.Equal(input, output) {
			t.Fatalf("round trip: %v\ninput=%v\noutput=%v", err, input, output)
		}
		if got := gitConfigRepos("", []*v1.Trigger{output}); !reflect.DeepEqual(got, event.Repos) {
			t.Fatalf("label repositories not provisioned: %v", got)
		}
	}
	for name, label := range map[string]*gitLabelModel{
		"no_repos":  {OnAdded: types.BoolValue(true), PullRequests: types.BoolValue(true)},
		"no_event":  {Repos: mustStringList(t, ctx, []string{"example/repo"}), PullRequests: types.BoolValue(true)},
		"no_object": {Repos: mustStringList(t, ctx, []string{"example/repo"}), OnAdded: types.BoolValue(true)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := triggerModelToProto(ctx, &triggerModel{GitLabel: label}); err == nil {
				t.Fatal("invalid label trigger should fail before a request is sent")
			}
		})
	}
}

type workflowCaptureServer struct {
	v1connect.UnimplementedAutomationsServiceHandler
	definition *v1.AutomationWithOwner
	update     *v1.UpdateAutomationRequest
}

func (s *workflowCaptureServer) GetAutomation(context.Context, *connect.Request[v1.GetAutomationRequest]) (*connect.Response[v1.GetAutomationResponse], error) {
	return connect.NewResponse(&v1.GetAutomationResponse{Result: &v1.GetAutomationResponse_Workflow{Workflow: s.definition}}), nil
}

func (s *workflowCaptureServer) UpdateAutomation(_ context.Context, req *connect.Request[v1.UpdateAutomationRequest]) (*connect.Response[v1.UpdateAutomationResponse], error) {
	s.update = proto.Clone(req.Msg).(*v1.UpdateAutomationRequest)
	s.definition = proto.Clone(s.definition).(*v1.AutomationWithOwner)
	s.definition.Workflow.Workflow = req.Msg.Workflow
	s.definition.Workflow.Scope = req.Msg.GetScope()
	return connect.NewResponse(&v1.UpdateAutomationResponse{Workflow: s.definition}), nil
}

func TestImportedWorkflowScopeUpdatePreservesRouting(t *testing.T) {
	ctx := context.Background()
	wf := &v1.Workflow{
		Prompts: []*v1.Prompt{{Prompt: "review"}}, Model: proto.String("composer-2.5"),
		Triggers: []*v1.Trigger{
			{Trigger: &v1.Trigger_SlackReactionAdded{SlackReactionAdded: &v1.SlackReactionAddedTrigger{Channel: "C001", Channels: []string{"C001", "C002", "C003", "C004", "C005"}, EmojiName: "jira"}}},
			{Trigger: &v1.Trigger_SlackTrigger{SlackTrigger: &v1.SlackTrigger{Channel: "C001", Channels: []string{"C001", "C002"}, TopLevelOnly: proto.Bool(true)}}},
			{Trigger: &v1.Trigger_Git{Git: &v1.GitTrigger{Event: &v1.GitTrigger_Label{Label: &v1.GitLabelEvent{Repos: []string{"example/repo"}, LabelName: "cursor", OnAdded: true, PullRequests: true}}}}},
		},
		Actions:   []*v1.Action{{Action: &v1.Action_Slack{Slack: &v1.SlackAction{Channel: "C001", Channels: []string{"C001", "C002"}}}}},
		GitConfig: &v1.GitConfig{Repo: "example/repo", Repos: []string{"example/repo"}},
	}
	mock := &workflowCaptureServer{definition: &v1.AutomationWithOwner{Workflow: &v1.Automation{
		AutomationId: "11111111-1111-4111-8111-111111111111", Name: "Existing", Enabled: true,
		Scope: v1.AutomationScope_AUTOMATION_SCOPE_TEAM_VISIBLE, Workflow: wf,
	}}}
	_, handler := v1connect.NewAutomationsServiceHandler(mock)
	server := httptest.NewServer(handler)
	defer server.Close()
	r := &platformWorkflowResource{client: &apiClient{automations: v1connect.NewAutomationsServiceClient(server.Client(), server.URL)}}
	sch := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, sch)
	imported := &resource.ImportStateResponse{State: emptyState(ctx, sch.Schema)}
	r.ImportState(ctx, resource.ImportStateRequest{ID: mock.definition.Workflow.AutomationId}, imported)
	if imported.Diagnostics.HasError() {
		t.Fatal(imported.Diagnostics)
	}
	read := &resource.ReadResponse{State: imported.State}
	r.Read(ctx, resource.ReadRequest{State: imported.State}, read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	plan := tfsdk.Plan{Schema: sch.Schema, Raw: read.State.Raw}
	if diags := plan.SetAttribute(ctx, path.Root("scope"), types.StringValue("team")); diags.HasError() {
		t.Fatal(diags)
	}
	updated := &resource.UpdateResponse{State: read.State}
	r.Update(ctx, resource.UpdateRequest{State: read.State, Plan: plan}, updated)
	if updated.Diagnostics.HasError() {
		t.Fatal(updated.Diagnostics)
	}
	if mock.update.GetScope() != v1.AutomationScope_AUTOMATION_SCOPE_TEAM || !proto.Equal(wf, mock.update.Workflow) {
		t.Fatalf("scope-only update changed workflow:\ninput=%v\noutput=%v", wf, mock.update)
	}
}

func TestSlackChannelsPlanPreservesImportedLists(t *testing.T) {
	ctx := context.Background()
	channels := mustStringList(t, ctx, []string{"C001", "C002"})
	m, err := protoToModel(ctx, &v1.AutomationWithOwner{Workflow: &v1.Automation{Workflow: &v1.Workflow{
		Triggers: []*v1.Trigger{{Trigger: &v1.Trigger_SlackTrigger{SlackTrigger: &v1.SlackTrigger{Channel: "C001", Channels: []string{"C001", "C002"}}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	r := &platformWorkflowResource{}
	sch := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, sch)
	base := tfsdk.Plan{Schema: sch.Schema}
	if diags := base.Set(ctx, &m); diags.HasError() {
		t.Fatal(diags)
	}
	state := tfsdk.State{Schema: sch.Schema, Raw: base.Raw}
	channelsPath := path.Root("trigger").AtListIndex(0).AtName("slack").AtName("channels")
	for _, tc := range []struct {
		name, configured string
		want             []string
	}{
		{"unchanged_legacy", "C001", []string{"C001", "C002"}},
		{"changed_legacy", "C003", []string{"C003"}},
		{"list_only", "", []string{"C001", "C002"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := tfsdk.Config{Schema: sch.Schema, Raw: base.Raw}
			configPlan := tfsdk.Plan{Schema: sch.Schema, Raw: config.Raw}
			channel := types.StringNull()
			if tc.configured != "" {
				channel = types.StringValue(tc.configured)
			}
			if diags := configPlan.SetAttribute(ctx, channelsPath.ParentPath().AtName("channel"), channel); diags.HasError() {
				t.Fatal(diags)
			}
			if diags := configPlan.SetAttribute(ctx, channelsPath, types.ListNull(types.StringType)); diags.HasError() {
				t.Fatal(diags)
			}
			config.Raw = configPlan.Raw
			req := planmodifier.ListRequest{Path: channelsPath, Config: config, Plan: base, State: state,
				ConfigValue: types.ListNull(types.StringType), PlanValue: types.ListUnknown(types.StringType), StateValue: channels}
			resp := &planmodifier.ListResponse{PlanValue: req.PlanValue}
			slackChannelsUseStateUnlessChannelChanged{}.PlanModifyList(ctx, req, resp)
			if resp.Diagnostics.HasError() || !resp.PlanValue.Equal(mustStringList(t, ctx, tc.want)) {
				t.Fatalf("plan=%v diagnostics=%v", resp.PlanValue, resp.Diagnostics)
			}
		})
	}
}
