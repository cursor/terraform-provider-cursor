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
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/defaults"
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
	ctx := context.Background()
	input := &v1.Action{Action: &v1.Action_Slack{Slack: &v1.SlackAction{
		Channel: "C001", Channels: []string{"C001", "C002"}, RespondInThread: true,
	}}}
	m := protoActionToModel(ctx, input)
	output, err := actionModelToProto(ctx, &m)
	if err != nil || !proto.Equal(input, output) {
		t.Fatalf("round trip: %v\ninput=%v\noutput=%v", err, input, output)
	}
	if output.GetSlack().GetGeneralized() {
		t.Fatal("multi-channel allowlist must not enable generalized Slack access")
	}
}

// Server data that predates `channels`, or that a team API key stored without
// normalisation, must read back in the provider's canonical form: channels is
// the server's effective routing list and channel is channels[0].
func TestSlackChannelsReadCanonicalForm(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name         string
		channel      string
		channels     []string
		wantChannel  string
		wantChannels []string
	}{
		{"legacy_scalar_only", "C999", nil, "C999", []string{"C999"}},
		{"list_wins_over_mismatched_scalar", "C999", []string{"C001", "C002"}, "C001", []string{"C001", "C002"}},
		{"list_only", "", []string{"C001", "C002"}, "C001", []string{"C001", "C002"}},
		{"blank_entries_ignored", "C999", []string{" ", ""}, "C999", []string{"C999"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := mustStringList(t, ctx, tc.wantChannels)
			for name, input := range map[string]*v1.Trigger{
				"message":      {Trigger: &v1.Trigger_SlackTrigger{SlackTrigger: &v1.SlackTrigger{Channel: tc.channel, Channels: tc.channels}}},
				"reaction":     {Trigger: &v1.Trigger_SlackReactionAdded{SlackReactionAdded: &v1.SlackReactionAddedTrigger{Channel: tc.channel, Channels: tc.channels, EmojiName: "eyes"}}},
				"mention":      {Trigger: &v1.Trigger_SlackMention{SlackMention: &v1.SlackMentionTrigger{Channel: tc.channel, Channels: tc.channels}}},
				"any_reaction": {Trigger: &v1.Trigger_SlackAnyReactionAdded{SlackAnyReactionAdded: &v1.SlackAnyReactionAddedTrigger{Channel: tc.channel, Channels: tc.channels}}},
			} {
				m, err := protoTriggerToModel(ctx, input)
				if err != nil {
					t.Fatal(err)
				}
				var channel types.String
				var channels types.List
				switch name {
				case "message":
					channel, channels = m.Slack.Channel, m.Slack.Channels
				case "reaction":
					channel, channels = m.SlackReactionAdded.Channel, m.SlackReactionAdded.Channels
				case "mention":
					channel, channels = m.SlackMention.Channel, m.SlackMention.Channels
				case "any_reaction":
					channel, channels = m.SlackAnyReactionAdded.Channel, m.SlackAnyReactionAdded.Channels
				}
				if channel.ValueString() != tc.wantChannel || !channels.Equal(want) {
					t.Fatalf("%s: channel=%v channels=%v", name, channel, channels)
				}
			}
			am := protoActionToModel(ctx, &v1.Action{Action: &v1.Action_Slack{Slack: &v1.SlackAction{Channel: tc.channel, Channels: tc.channels}}})
			if am.Slack.Channel.ValueString() != tc.wantChannel || !am.Slack.Channels.Equal(want) {
				t.Fatalf("action: channel=%v channels=%v", am.Slack.Channel, am.Slack.Channels)
			}
		})
	}
	am := protoActionToModel(ctx, &v1.Action{Action: &v1.Action_Slack{Slack: &v1.SlackAction{Generalized: true}}})
	if !am.Slack.Channel.IsNull() || !am.Slack.Channels.IsNull() {
		t.Fatalf("generalized action without channels must read back null: %v %v", am.Slack.Channel, am.Slack.Channels)
	}
}

func TestSlackChannelsForRequest(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name         string
		channel      types.String
		channels     []string // nil means not set
		required     bool
		wantChannel  string
		wantChannels []string
		wantError    string
	}{
		{"legacy_scalar", types.StringValue("C001"), nil, true, "C001", []string{"C001"}, ""},
		{"list_only", types.StringNull(), []string{"C001", "C002"}, true, "C001", []string{"C001", "C002"}, ""},
		{"matching_pair", types.StringValue("C001"), []string{"C001", "C002"}, true, "C001", []string{"C001", "C002"}, ""},
		{"unknown_scalar_uses_list", types.StringUnknown(), []string{"C001"}, true, "C001", []string{"C001"}, ""},
		{"mismatched_pair", types.StringValue("C999"), []string{"C001", "C002"}, true, "", nil, "must equal channels[0]"},
		{"empty_list", types.StringValue("C001"), []string{}, true, "", nil, "at least one channel"},
		{"missing_required", types.StringNull(), nil, true, "", nil, "requires channels"},
		{"missing_optional", types.StringNull(), nil, false, "", nil, ""},
		{"blank_entry", types.StringNull(), []string{" "}, true, "", nil, "must not be empty"},
		{"duplicate", types.StringNull(), []string{"C001", "C001"}, true, "", nil, "duplicates"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := types.ListNull(types.StringType)
			if tc.channels != nil {
				list = mustStringList(t, ctx, tc.channels)
			}
			channel, channels, err := slackChannelsForRequest(ctx, tc.channel, list, "slack", tc.required)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error=%v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || channel != tc.wantChannel || !reflect.DeepEqual(channels, tc.wantChannels) {
				t.Fatalf("got %q %v %v", channel, channels, err)
			}
		})
	}
}

// modelFromWorkflow builds a fully typed model (every list carries its element
// type) through the real proto conversion, so tests can set it on a plan.
func modelFromWorkflow(t *testing.T, ctx context.Context, wf *v1.Workflow) platformWorkflowModel {
	t.Helper()
	m, err := protoToModel(ctx, &v1.AutomationWithOwner{Workflow: &v1.Automation{
		AutomationId: "11111111-1111-4111-8111-111111111111", Name: "Slack", Enabled: true,
		Scope: v1.AutomationScope_AUTOMATION_SCOPE_USER, Workflow: wf,
	}})
	if err != nil {
		t.Fatal(err)
	}
	m.ID = types.StringNull()
	return m
}

func slackTriggerConfig(t *testing.T, ctx context.Context, sch tfsdk.Plan, channel types.String, channels types.List) (tfsdk.Config, path.Path) {
	t.Helper()
	plan := tfsdk.Plan{Schema: sch.Schema}
	m := modelFromWorkflow(t, ctx, &v1.Workflow{Triggers: []*v1.Trigger{{Trigger: &v1.Trigger_SlackTrigger{SlackTrigger: &v1.SlackTrigger{Channel: "C001"}}}}})
	m.Triggers[0].Slack.Channel, m.Triggers[0].Slack.Channels = channel, channels
	if diags := plan.Set(ctx, &m); diags.HasError() {
		t.Fatal(diags)
	}
	return tfsdk.Config{Schema: sch.Schema, Raw: plan.Raw}, path.Root("trigger").AtListIndex(0).AtName("slack")
}

func TestSlackChannelPlanModifiersDeriveEachOther(t *testing.T) {
	ctx := context.Background()
	r := &platformWorkflowResource{}
	sch := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, sch)
	base := tfsdk.Plan{Schema: sch.Schema}
	unknownList := types.ListUnknown(types.StringType)
	unknownFirst, _ := types.ListValue(types.StringType, []attr.Value{types.StringUnknown(), types.StringValue("C002")})

	for _, tc := range []struct {
		name         string
		channel      types.String
		channels     types.List
		wantChannel  types.String
		wantChannels types.List
	}{
		{"channel_only", types.StringValue("C001"), types.ListNull(types.StringType), types.StringValue("C001"), mustStringList(t, ctx, []string{"C001"})},
		{"channels_only", types.StringNull(), mustStringList(t, ctx, []string{"C001", "C002"}), types.StringValue("C001"), mustStringList(t, ctx, []string{"C001", "C002"})},
		{"both", types.StringValue("C001"), mustStringList(t, ctx, []string{"C001", "C002"}), types.StringValue("C001"), mustStringList(t, ctx, []string{"C001", "C002"})},
		{"neither", types.StringNull(), types.ListNull(types.StringType), types.StringNull(), types.ListNull(types.StringType)},
		{"unknown_channel", types.StringUnknown(), types.ListNull(types.StringType), types.StringUnknown(), unknownList},
		{"unknown_channels", types.StringNull(), unknownList, types.StringUnknown(), unknownList},
		{"unknown_first_channel", types.StringNull(), unknownFirst, types.StringUnknown(), unknownFirst},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, blockPath := slackTriggerConfig(t, ctx, base, tc.channel, tc.channels)
			plan := tfsdk.Plan{Schema: sch.Schema, Raw: config.Raw}

			// The framework hands computed attributes with a null config to the
			// modifiers as unknown.
			planChannels := tc.channels
			if planChannels.IsNull() {
				planChannels = unknownList
			}
			listResp := &planmodifier.ListResponse{PlanValue: planChannels}
			slackChannelsFromChannel{}.PlanModifyList(ctx, planmodifier.ListRequest{
				Path: blockPath.AtName("channels"), Config: config, Plan: plan, State: tfsdk.State{Schema: sch.Schema, Raw: config.Raw},
				ConfigValue: tc.channels, PlanValue: planChannels, StateValue: types.ListNull(types.StringType),
			}, listResp)
			if listResp.Diagnostics.HasError() || !listResp.PlanValue.Equal(tc.wantChannels) {
				t.Fatalf("channels plan=%v want=%v diags=%v", listResp.PlanValue, tc.wantChannels, listResp.Diagnostics)
			}

			planChannel := tc.channel
			if planChannel.IsNull() {
				planChannel = types.StringUnknown()
			}
			stringResp := &planmodifier.StringResponse{PlanValue: planChannel}
			slackChannelFromChannels{}.PlanModifyString(ctx, planmodifier.StringRequest{
				Path: blockPath.AtName("channel"), Config: config, Plan: plan, State: tfsdk.State{Schema: sch.Schema, Raw: config.Raw},
				ConfigValue: tc.channel, PlanValue: planChannel, StateValue: types.StringNull(),
			}, stringResp)
			if stringResp.Diagnostics.HasError() || !stringResp.PlanValue.Equal(tc.wantChannel) {
				t.Fatalf("channel plan=%v want=%v diags=%v", stringResp.PlanValue, tc.wantChannel, stringResp.Diagnostics)
			}
		})
	}
}

func TestValidateConfigSlackChannels(t *testing.T) {
	ctx := context.Background()
	r := &platformWorkflowResource{}
	sch := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, sch)
	unknownFirst, _ := types.ListValue(types.StringType, []attr.Value{types.StringUnknown()})

	validate := func(t *testing.T, m platformWorkflowModel) string {
		t.Helper()
		plan := tfsdk.Plan{Schema: sch.Schema}
		if diags := plan.Set(ctx, &m); diags.HasError() {
			t.Fatal(diags)
		}
		resp := &resource.ValidateConfigResponse{}
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sch.Schema, Raw: plan.Raw}}, resp)
		if !resp.Diagnostics.HasError() {
			return ""
		}
		var messages []string
		for _, d := range resp.Diagnostics.Errors() {
			messages = append(messages, d.Summary()+": "+d.Detail())
		}
		return strings.Join(messages, "\n")
	}
	trigger := func(channel types.String, channels types.List) platformWorkflowModel {
		m := modelFromWorkflow(t, ctx, &v1.Workflow{Triggers: []*v1.Trigger{{Trigger: &v1.Trigger_SlackMention{SlackMention: &v1.SlackMentionTrigger{Channel: "C001"}}}}})
		m.Triggers[0].SlackMention.Channel, m.Triggers[0].SlackMention.Channels = channel, channels
		return m
	}
	action := func(channel types.String, channels types.List) platformWorkflowModel {
		m := modelFromWorkflow(t, ctx, &v1.Workflow{Actions: []*v1.Action{{Action: &v1.Action_Slack{Slack: &v1.SlackAction{Generalized: true}}}}})
		m.Actions[0].Slack.Channel, m.Actions[0].Slack.Channels = channel, channels
		return m
	}
	label := func(flags *v1.GitLabelEvent) platformWorkflowModel {
		flags.Repos = []string{"example/repo"}
		return modelFromWorkflow(t, ctx, &v1.Workflow{Triggers: []*v1.Trigger{{Trigger: &v1.Trigger_Git{Git: &v1.GitTrigger{Event: &v1.GitTrigger_Label{Label: flags}}}}}})
	}

	for _, tc := range []struct {
		name      string
		model     platformWorkflowModel
		wantError string
	}{
		{"trigger_channel_only", trigger(types.StringValue("C001"), types.ListNull(types.StringType)), ""},
		{"trigger_channels_only", trigger(types.StringNull(), mustStringList(t, ctx, []string{"C001", "C002"})), ""},
		{"trigger_matching_pair", trigger(types.StringValue("C001"), mustStringList(t, ctx, []string{"C001", "C002"})), ""},
		{"trigger_mismatch", trigger(types.StringValue("C999"), mustStringList(t, ctx, []string{"C001", "C002"})), "channel does not match channels[0]"},
		{"trigger_empty_list", trigger(types.StringValue("C001"), mustStringList(t, ctx, []string{})), "Empty Slack channel list"},
		{"trigger_neither", trigger(types.StringNull(), types.ListNull(types.StringType)), "Missing Slack channel"},
		{"trigger_duplicate", trigger(types.StringNull(), mustStringList(t, ctx, []string{"C001", "C001"})), "Duplicate Slack channel ID"},
		{"trigger_blank", trigger(types.StringNull(), mustStringList(t, ctx, []string{"C001", " "})), "Blank Slack channel ID"},
		{"trigger_unknown_list", trigger(types.StringValue("C001"), types.ListUnknown(types.StringType)), ""},
		{"trigger_unknown_first", trigger(types.StringValue("C001"), unknownFirst), ""},
		{"action_neither_generalized", action(types.StringNull(), types.ListNull(types.StringType)), ""},
		{"action_mismatch", action(types.StringValue("C999"), mustStringList(t, ctx, []string{"C001"})), "channel does not match channels[0]"},
		{"action_empty_list", action(types.StringNull(), mustStringList(t, ctx, []string{})), "Empty Slack channel list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := validate(t, tc.model)
			if tc.wantError == "" && got != "" {
				t.Fatalf("unexpected error: %s", got)
			}
			if tc.wantError != "" && !strings.Contains(got, tc.wantError) {
				t.Fatalf("error=%q, want %q", got, tc.wantError)
			}
		})
	}

	t.Run("git_label_never_fires", func(t *testing.T) {
		if got := validate(t, label(&v1.GitLabelEvent{PullRequests: true})); !strings.Contains(got, "on_added or on_removed") {
			t.Fatalf("error=%q", got)
		}
		if got := validate(t, label(&v1.GitLabelEvent{OnAdded: true})); !strings.Contains(got, "pull_requests or issues") {
			t.Fatalf("error=%q", got)
		}
		if got := validate(t, label(&v1.GitLabelEvent{OnAdded: true, Issues: true})); got != "" {
			t.Fatalf("unexpected error: %s", got)
		}
		unknown := label(&v1.GitLabelEvent{Issues: true})
		unknown.Triggers[0].GitLabel.OnAdded = types.BoolUnknown()
		if got := validate(t, unknown); got != "" {
			t.Fatalf("unknown flags must not be rejected: %s", got)
		}
	})
}

// git_label flags default to false rather than carrying the prior state, so
// dropping a flag from config disables it, and an explicit false reads back
// unchanged (the server stores concrete proto3 booleans).
func TestGitLabelFlagsDefaultToFalse(t *testing.T) {
	ctx := context.Background()
	r := &platformWorkflowResource{}
	sch := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, sch)
	trigger := sch.Schema.Attributes["trigger"].(schema.ListNestedAttribute)
	label := trigger.NestedObject.Attributes["git_label"].(schema.SingleNestedAttribute)
	for _, name := range []string{"on_added", "on_removed", "pull_requests", "issues"} {
		flag := label.Attributes[name].(schema.BoolAttribute)
		if !flag.Optional || !flag.Computed || flag.Default == nil || len(flag.PlanModifiers) != 0 {
			t.Fatalf("%s must be Optional+Computed with a static default and no state-carrying plan modifier: %+v", name, flag)
		}
		resp := defaults.BoolResponse{}
		flag.Default.DefaultBool(ctx, defaults.BoolRequest{}, &resp)
		if !resp.PlanValue.Equal(types.BoolValue(false)) {
			t.Fatalf("%s default = %v, want false", name, resp.PlanValue)
		}
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
		// Flags are Computed and read back as concrete booleans, so an explicit
		// false in config matches the stored value.
		if !m.GitLabel.OnRemoved.Equal(types.BoolValue(event.OnRemoved)) || !m.GitLabel.Issues.Equal(types.BoolValue(event.Issues)) {
			t.Fatalf("on_removed=%v issues=%v for %v", m.GitLabel.OnRemoved, m.GitLabel.Issues, event)
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

// workflowCaptureServer is an in-process AutomationsService that records the
// last write and serves it back. normalize mimics a save made with a user
// token, where the server rewrites channel = channels[0] and materialises
// channels (resolveSlackChannelsInWorkflow); without it the workflow is
// stored verbatim, as for a team API key.
type workflowCaptureServer struct {
	v1connect.UnimplementedAutomationsServiceHandler
	definition *v1.AutomationWithOwner
	normalize  bool
	create     *v1.CreateAutomationRequest
	update     *v1.UpdateAutomationRequest
}

func (s *workflowCaptureServer) store(workflow *v1.Workflow) {
	if s.normalize {
		for _, trigger := range workflow.GetTriggers() {
			switch tr := trigger.GetTrigger().(type) {
			case *v1.Trigger_SlackTrigger:
				tr.SlackTrigger.Channels = slackChannelList(tr.SlackTrigger.GetChannel(), tr.SlackTrigger.GetChannels())
				tr.SlackTrigger.Channel = firstSlackChannel(tr.SlackTrigger.GetChannel(), tr.SlackTrigger.GetChannels())
			case *v1.Trigger_SlackReactionAdded:
				tr.SlackReactionAdded.Channels = slackChannelList(tr.SlackReactionAdded.GetChannel(), tr.SlackReactionAdded.GetChannels())
				tr.SlackReactionAdded.Channel = firstSlackChannel(tr.SlackReactionAdded.GetChannel(), tr.SlackReactionAdded.GetChannels())
			case *v1.Trigger_SlackMention:
				tr.SlackMention.Channels = slackChannelList(tr.SlackMention.GetChannel(), tr.SlackMention.GetChannels())
				tr.SlackMention.Channel = firstSlackChannel(tr.SlackMention.GetChannel(), tr.SlackMention.GetChannels())
			case *v1.Trigger_SlackAnyReactionAdded:
				tr.SlackAnyReactionAdded.Channels = slackChannelList(tr.SlackAnyReactionAdded.GetChannel(), tr.SlackAnyReactionAdded.GetChannels())
				tr.SlackAnyReactionAdded.Channel = firstSlackChannel(tr.SlackAnyReactionAdded.GetChannel(), tr.SlackAnyReactionAdded.GetChannels())
			}
		}
		for _, action := range workflow.GetActions() {
			if slack := action.GetSlack(); slack != nil && !slack.GetGeneralized() {
				slack.Channels = slackChannelList(slack.GetChannel(), slack.GetChannels())
				slack.Channel = firstSlackChannel(slack.GetChannel(), slack.GetChannels())
			}
		}
	}
	if s.definition == nil {
		s.definition = &v1.AutomationWithOwner{Workflow: &v1.Automation{
			AutomationId: "11111111-1111-4111-8111-111111111111", Enabled: true,
			Scope: v1.AutomationScope_AUTOMATION_SCOPE_USER,
		}}
	}
	s.definition = proto.Clone(s.definition).(*v1.AutomationWithOwner)
	s.definition.Workflow.Workflow = workflow
}

func (s *workflowCaptureServer) GetAutomation(context.Context, *connect.Request[v1.GetAutomationRequest]) (*connect.Response[v1.GetAutomationResponse], error) {
	return connect.NewResponse(&v1.GetAutomationResponse{Result: &v1.GetAutomationResponse_Workflow{Workflow: s.definition}}), nil
}

func (s *workflowCaptureServer) CreateAutomation(_ context.Context, req *connect.Request[v1.CreateAutomationRequest]) (*connect.Response[v1.CreateAutomationResponse], error) {
	s.create = proto.Clone(req.Msg).(*v1.CreateAutomationRequest)
	s.store(proto.Clone(req.Msg.GetWorkflow()).(*v1.Workflow))
	s.definition.Workflow.Name = req.Msg.GetName()
	s.definition.Workflow.Scope = req.Msg.GetScope()
	return connect.NewResponse(&v1.CreateAutomationResponse{Workflow: s.definition}), nil
}

func (s *workflowCaptureServer) UpdateAutomation(_ context.Context, req *connect.Request[v1.UpdateAutomationRequest]) (*connect.Response[v1.UpdateAutomationResponse], error) {
	s.update = proto.Clone(req.Msg).(*v1.UpdateAutomationRequest)
	s.store(proto.Clone(req.Msg.GetWorkflow()).(*v1.Workflow))
	if req.Msg.Scope != nil {
		s.definition.Workflow.Scope = req.Msg.GetScope()
	}
	if req.Msg.Name != nil {
		s.definition.Workflow.Name = req.Msg.GetName()
	}
	return connect.NewResponse(&v1.UpdateAutomationResponse{Workflow: s.definition}), nil
}

func newCaptureResource(t *testing.T, mock *workflowCaptureServer) (*platformWorkflowResource, *resource.SchemaResponse) {
	t.Helper()
	_, handler := v1connect.NewAutomationsServiceHandler(mock)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	r := &platformWorkflowResource{client: &apiClient{automations: v1connect.NewAutomationsServiceClient(server.Client(), server.URL)}}
	sch := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, sch)
	return r, sch
}

// After Create and Update the stored state must equal the planned channel /
// channels pair whether or not the server normalised the save, because the
// provider already sends the server's canonical form.
func TestSlackChannelsReadBackMatchesPlan(t *testing.T) {
	ctx := context.Background()
	for _, normalize := range []bool{true, false} {
		for _, tc := range []struct {
			name     string
			channels []string
		}{
			{"legacy_single", []string{"C001"}},
			{"multi", []string{"C001", "C002", "C003"}},
		} {
			name := tc.name + "/passthrough"
			if normalize {
				name = tc.name + "/normalized"
			}
			t.Run(name, func(t *testing.T) {
				mock := &workflowCaptureServer{normalize: normalize}
				r, sch := newCaptureResource(t, mock)
				channels := mustStringList(t, ctx, tc.channels)
				channel := types.StringValue(tc.channels[0])
				planModel := modelFromWorkflow(t, ctx, &v1.Workflow{
					Prompts: []*v1.Prompt{{Prompt: "review"}},
					Triggers: []*v1.Trigger{
						{Trigger: &v1.Trigger_SlackTrigger{SlackTrigger: &v1.SlackTrigger{Channel: tc.channels[0], Channels: tc.channels, TopLevelOnly: proto.Bool(true), SlackCompletionReactionMode: v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_OFF.Enum()}}},
						{Trigger: &v1.Trigger_SlackReactionAdded{SlackReactionAdded: &v1.SlackReactionAddedTrigger{Channel: tc.channels[0], Channels: tc.channels, EmojiName: "eyes"}}},
						{Trigger: &v1.Trigger_SlackMention{SlackMention: &v1.SlackMentionTrigger{Channel: tc.channels[0], Channels: tc.channels}}},
						{Trigger: &v1.Trigger_SlackAnyReactionAdded{SlackAnyReactionAdded: &v1.SlackAnyReactionAddedTrigger{Channel: tc.channels[0], Channels: tc.channels}}},
					},
					Actions: []*v1.Action{{Action: &v1.Action_Slack{Slack: &v1.SlackAction{Channel: tc.channels[0], Channels: tc.channels}}}},
				})
				plan := tfsdk.Plan{Schema: sch.Schema}
				if diags := plan.Set(ctx, &planModel); diags.HasError() {
					t.Fatal(diags)
				}
				created := &resource.CreateResponse{State: emptyState(ctx, sch.Schema)}
				r.Create(ctx, resource.CreateRequest{Plan: plan}, created)
				if created.Diagnostics.HasError() {
					t.Fatal(created.Diagnostics)
				}
				assertSlackChannelsEqual(t, ctx, created.State, channel, channels)
				if got := mock.create.GetWorkflow().GetTriggers()[0].GetSlackTrigger(); got.GetChannel() != tc.channels[0] || !reflect.DeepEqual(got.GetChannels(), tc.channels) {
					t.Fatalf("request not in canonical form: %v", got)
				}

				// Update to a different list; the deprecated scalar follows.
				next := []string{"C777", "C888"}
				planModel.Triggers[0].Slack.Channel = types.StringValue("C777")
				planModel.Triggers[0].Slack.Channels = mustStringList(t, ctx, next)
				planModel.Actions[0].Slack.Channel = types.StringValue("C777")
				planModel.Actions[0].Slack.Channels = mustStringList(t, ctx, next)
				planModel.ID = types.StringValue(mock.definition.Workflow.AutomationId)
				if diags := plan.Set(ctx, &planModel); diags.HasError() {
					t.Fatal(diags)
				}
				updated := &resource.UpdateResponse{State: created.State}
				r.Update(ctx, resource.UpdateRequest{Plan: plan, State: created.State}, updated)
				if updated.Diagnostics.HasError() {
					t.Fatal(updated.Diagnostics)
				}
				var state platformWorkflowModel
				if diags := updated.State.Get(ctx, &state); diags.HasError() {
					t.Fatal(diags)
				}
				wantNext := mustStringList(t, ctx, next)
				if state.Triggers[0].Slack.Channel.ValueString() != "C777" || !state.Triggers[0].Slack.Channels.Equal(wantNext) ||
					state.Actions[0].Slack.Channel.ValueString() != "C777" || !state.Actions[0].Slack.Channels.Equal(wantNext) {
					t.Fatalf("update read-back: trigger=%v/%v action=%v/%v", state.Triggers[0].Slack.Channel, state.Triggers[0].Slack.Channels, state.Actions[0].Slack.Channel, state.Actions[0].Slack.Channels)
				}
			})
		}
	}
}

func assertSlackChannelsEqual(t *testing.T, ctx context.Context, state tfsdk.State, channel types.String, channels types.List) {
	t.Helper()
	var m platformWorkflowModel
	if diags := state.Get(ctx, &m); diags.HasError() {
		t.Fatal(diags)
	}
	got := [][2]attr.Value{
		{m.Triggers[0].Slack.Channel, m.Triggers[0].Slack.Channels},
		{m.Triggers[1].SlackReactionAdded.Channel, m.Triggers[1].SlackReactionAdded.Channels},
		{m.Triggers[2].SlackMention.Channel, m.Triggers[2].SlackMention.Channels},
		{m.Triggers[3].SlackAnyReactionAdded.Channel, m.Triggers[3].SlackAnyReactionAdded.Channels},
		{m.Actions[0].Slack.Channel, m.Actions[0].Slack.Channels},
	}
	for i, pair := range got {
		if !pair[0].Equal(channel) || !pair[1].Equal(channels) {
			t.Fatalf("block %d: channel=%v channels=%v, want %v %v", i, pair[0], pair[1], channel, channels)
		}
	}
	if !m.Triggers[0].Slack.TopLevelOnly.Equal(types.BoolValue(true)) {
		t.Fatalf("top_level_only=%v", m.Triggers[0].Slack.TopLevelOnly)
	}
}

func TestImportedWorkflowScopeUpdatePreservesRouting(t *testing.T) {
	ctx := context.Background()
	wf := &v1.Workflow{
		Prompts: []*v1.Prompt{{Prompt: "review"}}, Model: proto.String("composer-2.5"),
		Triggers: []*v1.Trigger{
			{Trigger: &v1.Trigger_SlackReactionAdded{SlackReactionAdded: &v1.SlackReactionAddedTrigger{Channel: "C001", Channels: []string{"C001", "C002", "C003", "C004", "C005"}, EmojiName: "jira"}}},
			{Trigger: &v1.Trigger_SlackTrigger{SlackTrigger: &v1.SlackTrigger{Channel: "C001", Channels: []string{"C001", "C002"}, TopLevelOnly: proto.Bool(true), BlockUnauthenticatedSlackUsers: true, SlackCompletionReactionMode: v1.SlackCompletionReactionMode_SLACK_COMPLETION_REACTION_MODE_OFF.Enum()}}},
			{Trigger: &v1.Trigger_Git{Git: &v1.GitTrigger{Event: &v1.GitTrigger_Label{Label: &v1.GitLabelEvent{Repos: []string{"example/repo"}, LabelName: "cursor", OnAdded: true, PullRequests: true}}}}},
		},
		Actions:       []*v1.Action{{Action: &v1.Action_Slack{Slack: &v1.SlackAction{Channel: "C001", Channels: []string{"C001", "C002"}, RespondInThread: true}}}, {Action: &v1.Action_ReadSlack{ReadSlack: &v1.ReadSlackAction{}}}},
		GitConfig:     &v1.GitConfig{Repo: "example/repo", Repos: []string{"example/repo"}},
		MemoryEnabled: proto.Bool(false),
		AgentOptions:  &v1.AgentOptions{SkipInstall: proto.Bool(false), PrivateWorker: &v1.AgentPrivateWorkerConfig{Labels: []*v1.AgentPrivateWorkerLabel{{Key: "pool", Value: "example-workers"}}}},
	}
	mock := &workflowCaptureServer{definition: &v1.AutomationWithOwner{Workflow: &v1.Automation{
		AutomationId: "11111111-1111-4111-8111-111111111111", Name: "Existing", Enabled: true,
		Scope: v1.AutomationScope_AUTOMATION_SCOPE_TEAM_VISIBLE, Workflow: wf,
	}}}
	r, sch := newCaptureResource(t, mock)
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
	initialDefinition := proto.Clone(mock.definition).(*v1.AutomationWithOwner)
	for _, field := range []string{"scope", "name", "prompt"} {
		t.Run(field, func(t *testing.T) {
			mock.definition = proto.Clone(initialDefinition).(*v1.AutomationWithOwner)
			plan := tfsdk.Plan{Schema: sch.Schema, Raw: read.State.Raw}
			value := "updated"
			if field == "scope" {
				value = "team"
			}
			if diags := plan.SetAttribute(ctx, path.Root(field), types.StringValue(value)); diags.HasError() {
				t.Fatal(diags)
			}
			updated := &resource.UpdateResponse{State: read.State}
			r.Update(ctx, resource.UpdateRequest{State: read.State, Plan: plan}, updated)
			if updated.Diagnostics.HasError() {
				t.Fatal(updated.Diagnostics)
			}
			want := proto.Clone(wf).(*v1.Workflow)
			if field == "prompt" {
				want.Prompts[0].Prompt = value
			}
			if !proto.Equal(want, mock.update.Workflow) {
				t.Fatalf("%s-only update changed unrelated workflow fields:\ninput=%v\noutput=%v", field, want, mock.update)
			}
		})
	}
}
