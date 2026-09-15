package provider

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Origin rule types. Each one maps to exactly one typed block under rule.
// Merge rules are evaluated when a pull request merges (kind merge_branch);
// push rules are evaluated on ref updates (kind push_branch, push_tag,
// push_repository). The wire format is unchanged: ruleType plus a JSON
// parameters object whose keys are the camelCase form of the block attributes.
const (
	originRuleTypePullRequest           = "pull_request"
	originRuleTypeRequireStatusChecks   = "require_status_checks"
	originRuleTypeRequireBranchUpToDate = "require_branch_up_to_date"
	originRuleTypeDeletion              = "deletion"
	originRuleTypeNonFastForward        = "non_fast_forward"
	originRuleTypeBlockDirectUpdates    = "block_direct_updates"
	originRuleTypeBlockMerges           = "block_merges"
	originRuleTypeRequiredLinearHistory = "required_linear_history"
	originRuleTypeRefNamePattern        = "ref_name_pattern"

	maxOriginRequiredApprovingReviews = 50
)

type originRuleCategory int

const (
	originMergeRule originRuleCategory = iota
	originPushRule
)

type originRuleTypeSpec struct {
	ruleType string
	category originRuleCategory
}

// originRuleTypeSpecs is the ordered registry of typed rule blocks. The order
// is used for error messages and documentation only.
var originRuleTypeSpecs = []originRuleTypeSpec{
	{ruleType: originRuleTypePullRequest, category: originMergeRule},
	{ruleType: originRuleTypeRequireStatusChecks, category: originMergeRule},
	{ruleType: originRuleTypeRequireBranchUpToDate, category: originMergeRule},
	{ruleType: originRuleTypeDeletion, category: originPushRule},
	{ruleType: originRuleTypeNonFastForward, category: originPushRule},
	{ruleType: originRuleTypeBlockDirectUpdates, category: originPushRule},
	{ruleType: originRuleTypeBlockMerges, category: originPushRule},
	{ruleType: originRuleTypeRequiredLinearHistory, category: originPushRule},
	{ruleType: originRuleTypeRefNamePattern, category: originPushRule},
}

func originRuleTypeNames() []string {
	names := make([]string, 0, len(originRuleTypeSpecs))
	for _, spec := range originRuleTypeSpecs {
		names = append(names, spec.ruleType)
	}
	return names
}

func originRuleCategoryOf(ruleType string) (originRuleCategory, bool) {
	for _, spec := range originRuleTypeSpecs {
		if spec.ruleType == ruleType {
			return spec.category, true
		}
	}
	return 0, false
}

func originKindCategory(kind string) (originRuleCategory, bool) {
	switch kind {
	case originRulesetKindMergeBranch:
		return originMergeRule, true
	case originRulesetKindPushBranch, originRulesetKindPushTag, originRulesetKindPushRepository:
		return originPushRule, true
	default:
		return 0, false
	}
}

// originRepoRulesetRuleModel is one rule block. Exactly one typed block is set.
type originRepoRulesetRuleModel struct {
	ID                    types.String                        `tfsdk:"id"`
	PullRequest           *originPullRequestRuleModel         `tfsdk:"pull_request"`
	RequireStatusChecks   *originRequireStatusChecksRuleModel `tfsdk:"require_status_checks"`
	RequireBranchUpToDate *originEmptyRuleModel               `tfsdk:"require_branch_up_to_date"`
	Deletion              *originEmptyRuleModel               `tfsdk:"deletion"`
	NonFastForward        *originEmptyRuleModel               `tfsdk:"non_fast_forward"`
	BlockDirectUpdates    *originEmptyRuleModel               `tfsdk:"block_direct_updates"`
	BlockMerges           *originEmptyRuleModel               `tfsdk:"block_merges"`
	RequiredLinearHistory *originEmptyRuleModel               `tfsdk:"required_linear_history"`
	RefNamePattern        *originRefNamePatternRuleModel      `tfsdk:"ref_name_pattern"`
}

type originPullRequestRuleModel struct {
	RequiredApprovingReviewCount   types.Int64 `tfsdk:"required_approving_review_count"`
	DismissStaleReviewsOnPush      types.Bool  `tfsdk:"dismiss_stale_reviews_on_push"`
	RequireCodeOwnerReview         types.Bool  `tfsdk:"require_code_owner_review"`
	RequireLastPushApproval        types.Bool  `tfsdk:"require_last_push_approval"`
	RequiredReviewThreadResolution types.Bool  `tfsdk:"required_review_thread_resolution"`
}

type originRequireStatusChecksRuleModel struct {
	RequiredChecks []originRequiredCheckModel `tfsdk:"required_check"`
}

type originRequiredCheckModel struct {
	ActorKind types.String `tfsdk:"actor_kind"`
	ActorID   types.String `tfsdk:"actor_id"`
	GroupKey  types.String `tfsdk:"group_key"`
	RunKey    types.String `tfsdk:"run_key"`
	Name      types.String `tfsdk:"name"`
}

type originRefNamePatternRuleModel struct {
	Pattern types.String `tfsdk:"pattern"`
	Negate  types.Bool   `tfsdk:"negate"`
}

// originEmptyRuleModel is the body of a rule that takes no parameters, for
// example `deletion {}`.
type originEmptyRuleModel struct{}

// Wire shapes for the parameters object, keyed in camelCase.
type originPullRequestParameters struct {
	RequiredApprovingReviewCount   *int64 `json:"requiredApprovingReviewCount,omitempty"`
	DismissStaleReviewsOnPush      *bool  `json:"dismissStaleReviewsOnPush,omitempty"`
	RequireCodeOwnerReview         *bool  `json:"requireCodeOwnerReview,omitempty"`
	RequireLastPushApproval        *bool  `json:"requireLastPushApproval,omitempty"`
	RequiredReviewThreadResolution *bool  `json:"requiredReviewThreadResolution,omitempty"`
}

func (p originPullRequestParameters) empty() bool {
	return p.RequiredApprovingReviewCount == nil && p.DismissStaleReviewsOnPush == nil && p.RequireCodeOwnerReview == nil && p.RequireLastPushApproval == nil && p.RequiredReviewThreadResolution == nil
}

type originRequireStatusChecksParameters struct {
	RequiredChecks []originRequiredCheckParameters `json:"requiredChecks"`
}

type originRequiredCheckParameters struct {
	ActorKind string `json:"actorKind"`
	ActorID   string `json:"actorId"`
	GroupKey  string `json:"groupKey"`
	RunKey    string `json:"runKey,omitempty"`
	Name      string `json:"name,omitempty"`
}

type originRefNamePatternParameters struct {
	Pattern string `json:"pattern"`
	Negate  *bool  `json:"negate,omitempty"`
}

func originRuleBlocks() map[string]schema.Block {
	return map[string]schema.Block{
		originRuleTypePullRequest: schema.SingleNestedBlock{
			Description: "Merge rule: require a pull request before merging. Every argument is optional; omitted arguments use the Origin default. Rulesets of kind merge_branch only.",
			Attributes: map[string]schema.Attribute{
				"required_approving_review_count": schema.Int64Attribute{
					Optional:    true,
					Description: "Number of approving reviews required before the pull request can merge, from 0 to 50.",
				},
				"dismiss_stale_reviews_on_push": schema.BoolAttribute{
					Optional:    true,
					Description: "Dismiss existing approvals when new commits are pushed to the pull request.",
				},
				"require_code_owner_review": schema.BoolAttribute{
					Optional:    true,
					Description: "Require an approving review from a code owner of every changed file.",
				},
				"require_last_push_approval": schema.BoolAttribute{
					Optional:    true,
					Description: "Require the most recent push to be approved by someone other than the person who pushed it.",
				},
				"required_review_thread_resolution": schema.BoolAttribute{
					Optional:    true,
					Description: "Require every review thread to be resolved before merging.",
				},
			},
		},
		originRuleTypeRequireStatusChecks: schema.SingleNestedBlock{
			Description: "Merge rule: require the listed checks to pass on the head commit before merging. Rulesets of kind merge_branch only.",
			Blocks: map[string]schema.Block{
				"required_check": schema.ListNestedBlock{
					Description: "A check that must pass, identified by the actor that reports it and its group key. Repeat the block for each required check.",
					NestedObject: schema.NestedBlockObject{
						Attributes: map[string]schema.Attribute{
							"actor_kind": schema.StringAttribute{
								Required:    true,
								Description: "Kind of actor that reports the check, for example app.",
							},
							"actor_id": schema.StringAttribute{
								Required:    true,
								Description: "ID of the actor that reports the check, for example an Origin app ID (app_...).",
							},
							"group_key": schema.StringAttribute{
								Required:    true,
								Description: "Check group key the actor reports under.",
							},
							"run_key": schema.StringAttribute{
								Optional:    true,
								Description: "Specific check run key within the group. Omit it to require the group as a whole.",
							},
							"name": schema.StringAttribute{
								Optional:    true,
								Description: "Display name of the required check.",
							},
						},
					},
				},
			},
		},
		originRuleTypeRequireBranchUpToDate: schema.SingleNestedBlock{
			Description: "Merge rule: require the pull request head to be up to date with the base branch before merging. Takes no arguments; write `require_branch_up_to_date {}`. Rulesets of kind merge_branch only.",
		},
		originRuleTypeDeletion: schema.SingleNestedBlock{
			Description: "Push rule: block deleting matching refs. Takes no arguments; write `deletion {}`. Rulesets of kind push_branch, push_tag, or push_repository only.",
		},
		originRuleTypeNonFastForward: schema.SingleNestedBlock{
			Description: "Push rule: block force pushes to matching refs. Takes no arguments; write `non_fast_forward {}`. Rulesets of kind push_branch, push_tag, or push_repository only.",
		},
		originRuleTypeBlockDirectUpdates: schema.SingleNestedBlock{
			Description: "Push rule: require updates to matching refs to go through a pull request. Takes no arguments; write `block_direct_updates {}`, which stores the rule with Origin's default of blocking direct updates. Rulesets of kind push_branch, push_tag, or push_repository only.",
		},
		originRuleTypeBlockMerges: schema.SingleNestedBlock{
			Description: "Push rule: block pull request merges into matching refs. Takes no arguments; write `block_merges {}`. Rulesets of kind push_branch, push_tag, or push_repository only.",
		},
		originRuleTypeRequiredLinearHistory: schema.SingleNestedBlock{
			Description: "Push rule: block merge commits so matching refs keep a linear history. Takes no arguments; write `required_linear_history {}`. Rulesets of kind push_branch, push_tag, or push_repository only.",
		},
		originRuleTypeRefNamePattern: schema.SingleNestedBlock{
			Description: "Push rule: restrict ref names that can be created or updated. Rulesets of kind push_branch, push_tag, or push_repository only.",
			Attributes: map[string]schema.Attribute{
				"pattern": schema.StringAttribute{
					Optional:    true,
					Description: "Pattern the ref name is matched against. Required when the ref_name_pattern block is set.",
				},
				"negate": schema.BoolAttribute{
					Optional:    true,
					Description: "When true, ref names that match the pattern are rejected instead of required. Omit it to use the Origin default.",
				},
			},
		},
	}
}

// typedBlocks returns the names of the typed blocks set on this rule.
func (m originRepoRulesetRuleModel) typedBlocks() []string {
	var set []string
	if m.PullRequest != nil {
		set = append(set, originRuleTypePullRequest)
	}
	if m.RequireStatusChecks != nil {
		set = append(set, originRuleTypeRequireStatusChecks)
	}
	if m.RequireBranchUpToDate != nil {
		set = append(set, originRuleTypeRequireBranchUpToDate)
	}
	if m.Deletion != nil {
		set = append(set, originRuleTypeDeletion)
	}
	if m.NonFastForward != nil {
		set = append(set, originRuleTypeNonFastForward)
	}
	if m.BlockDirectUpdates != nil {
		set = append(set, originRuleTypeBlockDirectUpdates)
	}
	if m.BlockMerges != nil {
		set = append(set, originRuleTypeBlockMerges)
	}
	if m.RequiredLinearHistory != nil {
		set = append(set, originRuleTypeRequiredLinearHistory)
	}
	if m.RefNamePattern != nil {
		set = append(set, originRuleTypeRefNamePattern)
	}
	return set
}

// ruleType returns the Origin rule type of the single typed block on this
// rule, or an error when zero or several blocks are set.
func (m originRepoRulesetRuleModel) ruleType() (string, error) {
	set := m.typedBlocks()
	if len(set) != 1 {
		return "", fmt.Errorf("must set exactly one of %s", strings.Join(originRuleTypeNames(), ", "))
	}
	return set[0], nil
}

// validate checks the typed block on this rule against the ruleset kind.
func (m originRepoRulesetRuleModel) validate(name string, kind types.String) error {
	ruleType, err := m.ruleType()
	if err != nil {
		return fmt.Errorf("%s %w", name, err)
	}
	if !kind.IsNull() && !kind.IsUnknown() {
		kindCategory, kindKnown := originKindCategory(kind.ValueString())
		ruleCategory, _ := originRuleCategoryOf(ruleType)
		if kindKnown && kindCategory != ruleCategory {
			if ruleCategory == originMergeRule {
				return fmt.Errorf("%s.%s is a merge rule and requires kind %s", name, ruleType, originRulesetKindMergeBranch)
			}
			return fmt.Errorf("%s.%s is a push rule and requires kind %s, %s, or %s", name, ruleType, originRulesetKindPushBranch, originRulesetKindPushTag, originRulesetKindPushRepository)
		}
	}
	switch ruleType {
	case originRuleTypePullRequest:
		count := m.PullRequest.RequiredApprovingReviewCount
		if !count.IsNull() && !count.IsUnknown() && (count.ValueInt64() < 0 || count.ValueInt64() > maxOriginRequiredApprovingReviews) {
			return fmt.Errorf("%s.pull_request.required_approving_review_count must be between 0 and %d", name, maxOriginRequiredApprovingReviews)
		}
	case originRuleTypeRequireStatusChecks:
		for i, check := range m.RequireStatusChecks.RequiredChecks {
			prefix := fmt.Sprintf("%s.require_status_checks.required_check[%d]", name, i)
			if err := requireNonEmptyUnpadded(check.ActorKind, prefix+".actor_kind"); err != nil {
				return err
			}
			if err := requireNonEmptyUnpadded(check.ActorID, prefix+".actor_id"); err != nil {
				return err
			}
			if err := requireNonEmptyUnpadded(check.GroupKey, prefix+".group_key"); err != nil {
				return err
			}
			if !check.RunKey.IsNull() {
				if err := requireNonEmptyUnpadded(check.RunKey, prefix+".run_key"); err != nil {
					return err
				}
			}
			if !check.Name.IsNull() {
				if err := requireNonEmptyUnpadded(check.Name, prefix+".name"); err != nil {
					return err
				}
			}
		}
	case originRuleTypeRefNamePattern:
		if err := requireNonEmptyUnpadded(m.RefNamePattern.Pattern, name+".ref_name_pattern.pattern"); err != nil {
			return err
		}
	}
	return nil
}

// parameters encodes the typed block as the Origin parameters object. Rules
// without parameters return nil so the field is omitted on the wire.
func (m originRepoRulesetRuleModel) parameters() (json.RawMessage, error) {
	ruleType, err := m.ruleType()
	if err != nil {
		return nil, err
	}
	var wire any
	switch ruleType {
	case originRuleTypePullRequest:
		params := originPullRequestParameters{
			RequiredApprovingReviewCount:   int64Pointer(m.PullRequest.RequiredApprovingReviewCount),
			DismissStaleReviewsOnPush:      boolPointer(m.PullRequest.DismissStaleReviewsOnPush),
			RequireCodeOwnerReview:         boolPointer(m.PullRequest.RequireCodeOwnerReview),
			RequireLastPushApproval:        boolPointer(m.PullRequest.RequireLastPushApproval),
			RequiredReviewThreadResolution: boolPointer(m.PullRequest.RequiredReviewThreadResolution),
		}
		if params.empty() {
			return nil, nil
		}
		wire = params
	case originRuleTypeRequireStatusChecks:
		params := originRequireStatusChecksParameters{
			RequiredChecks: make([]originRequiredCheckParameters, 0, len(m.RequireStatusChecks.RequiredChecks)),
		}
		for i, check := range m.RequireStatusChecks.RequiredChecks {
			if check.ActorKind.IsUnknown() || check.ActorID.IsUnknown() || check.GroupKey.IsUnknown() || check.RunKey.IsUnknown() || check.Name.IsUnknown() {
				return nil, fmt.Errorf("require_status_checks.required_check[%d] is incomplete", i)
			}
			params.RequiredChecks = append(params.RequiredChecks, originRequiredCheckParameters{
				ActorKind: check.ActorKind.ValueString(),
				ActorID:   check.ActorID.ValueString(),
				GroupKey:  check.GroupKey.ValueString(),
				RunKey:    check.RunKey.ValueString(),
				Name:      check.Name.ValueString(),
			})
		}
		wire = params
	case originRuleTypeRefNamePattern:
		if m.RefNamePattern.Pattern.IsUnknown() || m.RefNamePattern.Negate.IsUnknown() {
			return nil, fmt.Errorf("ref_name_pattern is incomplete")
		}
		wire = originRefNamePatternParameters{
			Pattern: m.RefNamePattern.Pattern.ValueString(),
			Negate:  boolPointer(m.RefNamePattern.Negate),
		}
	default:
		return nil, nil
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encoding %s parameters: %w", ruleType, err)
	}
	return json.RawMessage(encoded), nil
}

func int64Pointer(value types.Int64) *int64 {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	v := value.ValueInt64()
	return &v
}

func boolPointer(value types.Bool) *bool {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	v := value.ValueBool()
	return &v
}

func int64FromPointer(value *int64) types.Int64 {
	if value == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*value)
}

func boolFromPointer(value *bool) types.Bool {
	if value == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*value)
}

// ruleModelFromAPI decodes an Origin rule into its typed block.
func ruleModelFromAPI(rule originRulesetRule) (originRepoRulesetRuleModel, error) {
	if strings.TrimSpace(rule.ID) == "" {
		return originRepoRulesetRuleModel{}, fmt.Errorf("Origin API returned a rule without an id")
	}
	model := originRepoRulesetRuleModel{ID: types.StringValue(rule.ID)}
	raw := rule.Parameters
	if len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	switch rule.RuleType {
	case originRuleTypePullRequest:
		var params originPullRequestParameters
		if err := json.Unmarshal(raw, &params); err != nil {
			return originRepoRulesetRuleModel{}, fmt.Errorf("rule %q parameters: %w", rule.ID, err)
		}
		model.PullRequest = &originPullRequestRuleModel{
			RequiredApprovingReviewCount:   int64FromPointer(params.RequiredApprovingReviewCount),
			DismissStaleReviewsOnPush:      boolFromPointer(params.DismissStaleReviewsOnPush),
			RequireCodeOwnerReview:         boolFromPointer(params.RequireCodeOwnerReview),
			RequireLastPushApproval:        boolFromPointer(params.RequireLastPushApproval),
			RequiredReviewThreadResolution: boolFromPointer(params.RequiredReviewThreadResolution),
		}
	case originRuleTypeRequireStatusChecks:
		var params originRequireStatusChecksParameters
		if err := json.Unmarshal(raw, &params); err != nil {
			return originRepoRulesetRuleModel{}, fmt.Errorf("rule %q parameters: %w", rule.ID, err)
		}
		block := &originRequireStatusChecksRuleModel{
			RequiredChecks: make([]originRequiredCheckModel, 0, len(params.RequiredChecks)),
		}
		for _, check := range params.RequiredChecks {
			block.RequiredChecks = append(block.RequiredChecks, originRequiredCheckModel{
				ActorKind: types.StringValue(check.ActorKind),
				ActorID:   types.StringValue(check.ActorID),
				GroupKey:  types.StringValue(check.GroupKey),
				RunKey:    stringOrNull(check.RunKey),
				Name:      stringOrNull(check.Name),
			})
		}
		model.RequireStatusChecks = block
	case originRuleTypeRefNamePattern:
		var params originRefNamePatternParameters
		if err := json.Unmarshal(raw, &params); err != nil {
			return originRepoRulesetRuleModel{}, fmt.Errorf("rule %q parameters: %w", rule.ID, err)
		}
		model.RefNamePattern = &originRefNamePatternRuleModel{
			Pattern: types.StringValue(params.Pattern),
			Negate:  boolFromPointer(params.Negate),
		}
	case originRuleTypeRequireBranchUpToDate:
		model.RequireBranchUpToDate = &originEmptyRuleModel{}
	case originRuleTypeDeletion:
		model.Deletion = &originEmptyRuleModel{}
	case originRuleTypeNonFastForward:
		model.NonFastForward = &originEmptyRuleModel{}
	case originRuleTypeBlockDirectUpdates:
		model.BlockDirectUpdates = &originEmptyRuleModel{}
	case originRuleTypeBlockMerges:
		model.BlockMerges = &originEmptyRuleModel{}
	case originRuleTypeRequiredLinearHistory:
		model.RequiredLinearHistory = &originEmptyRuleModel{}
	default:
		return originRepoRulesetRuleModel{}, fmt.Errorf("Origin API returned rule %q with type %q, which this provider does not support; supported types are %s", rule.ID, rule.RuleType, strings.Join(originRuleTypeNames(), ", "))
	}
	return model, nil
}

// ruleStateFromAPI maps an Origin rule onto the rule the configuration asked
// for. The rule ID always comes from Origin. On apply the typed block keeps the
// planned values, so state matches the plan. On refresh, attributes the
// configuration manages take the Origin value and attributes left unset stay
// unset, so server-side defaults do not show up as drift.
func ruleStateFromAPI(want originRepoRulesetRuleModel, rule originRulesetRule, apply bool) (originRepoRulesetRuleModel, error) {
	fromAPI, err := ruleModelFromAPI(rule)
	if err != nil {
		return originRepoRulesetRuleModel{}, err
	}
	if apply {
		want.ID = fromAPI.ID
		return want, nil
	}
	merged := fromAPI
	if want.PullRequest != nil && fromAPI.PullRequest != nil {
		block := *fromAPI.PullRequest
		block.RequiredApprovingReviewCount = mergeInt64(want.PullRequest.RequiredApprovingReviewCount, block.RequiredApprovingReviewCount)
		block.DismissStaleReviewsOnPush = mergeBool(want.PullRequest.DismissStaleReviewsOnPush, block.DismissStaleReviewsOnPush)
		block.RequireCodeOwnerReview = mergeBool(want.PullRequest.RequireCodeOwnerReview, block.RequireCodeOwnerReview)
		block.RequireLastPushApproval = mergeBool(want.PullRequest.RequireLastPushApproval, block.RequireLastPushApproval)
		block.RequiredReviewThreadResolution = mergeBool(want.PullRequest.RequiredReviewThreadResolution, block.RequiredReviewThreadResolution)
		merged.PullRequest = &block
	}
	if want.RefNamePattern != nil && fromAPI.RefNamePattern != nil {
		block := *fromAPI.RefNamePattern
		block.Negate = mergeBool(want.RefNamePattern.Negate, block.Negate)
		merged.RefNamePattern = &block
	}
	return merged, nil
}

// mergeInt64 applies the refresh rule for one optional attribute: unset in
// the prior state stays unset; otherwise the Origin value wins when present.
func mergeInt64(prior, api types.Int64) types.Int64 {
	if prior.IsNull() {
		return types.Int64Null()
	}
	if api.IsNull() {
		return prior
	}
	return api
}

func mergeBool(prior, api types.Bool) types.Bool {
	if prior.IsNull() {
		return types.BoolNull()
	}
	if api.IsNull() {
		return prior
	}
	return api
}

// ruleMatchesAPI reports whether an Origin rule has the same type as the
// configured rule and carries every parameter the configuration sets.
func ruleMatchesAPI(want originRepoRulesetRuleModel, rule originRulesetRule) bool {
	wantType, err := want.ruleType()
	if err != nil || wantType != rule.RuleType {
		return false
	}
	wantParams, err := want.parameters()
	if err != nil || wantParams == nil {
		return true
	}
	var wantMap, gotMap map[string]any
	if err := json.Unmarshal(wantParams, &wantMap); err != nil {
		return false
	}
	if len(rule.Parameters) == 0 {
		return len(wantMap) == 0
	}
	if err := json.Unmarshal(rule.Parameters, &gotMap); err != nil {
		return false
	}
	for key, value := range wantMap {
		if !reflect.DeepEqual(gotMap[key], value) {
			return false
		}
	}
	return true
}
