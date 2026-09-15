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
	originRuleTypeRequiredLinearHistory = "required_linear_history"
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
	{ruleType: originRuleTypeRequiredLinearHistory, category: originPushRule},
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
	RequiredLinearHistory *originEmptyRuleModel               `tfsdk:"required_linear_history"`
}

type originPullRequestRuleModel struct {
	RequiredApprovingReviewCount types.Int64 `tfsdk:"required_approving_review_count"`
}

type originRequireStatusChecksRuleModel struct {
	RequiredChecks []originRequiredCheckModel `tfsdk:"required_check"`
}

type originRequiredCheckModel struct {
	Name  types.String `tfsdk:"name"`
	AppID types.String `tfsdk:"app_id"`
}

// originEmptyRuleModel is the body of a rule that takes no parameters, for
// example `deletion {}`.
type originEmptyRuleModel struct{}

// Wire shapes for the parameters object, keyed in camelCase.
type originPullRequestParameters struct {
	RequiredApprovingReviewCount *int64 `json:"requiredApprovingReviewCount,omitempty"`
}

type originRequireStatusChecksParameters struct {
	RequiredChecks []originRequiredCheckParameters `json:"requiredChecks"`
}

type originRequiredCheckParameters struct {
	Name  string `json:"name"`
	AppID string `json:"appId,omitempty"`
}

func originRuleBlocks() map[string]schema.Block {
	return map[string]schema.Block{
		originRuleTypePullRequest: schema.SingleNestedBlock{
			Description: "Merge rule: require a pull request before merging. Rulesets of kind merge_branch only.",
			Attributes: map[string]schema.Attribute{
				"required_approving_review_count": schema.Int64Attribute{
					Optional:    true,
					Description: "Number of approving reviews required before the pull request can merge. Omit it to use the Origin default.",
				},
			},
		},
		originRuleTypeRequireStatusChecks: schema.SingleNestedBlock{
			Description: "Merge rule: require the listed check runs to pass on the head commit before merging. Rulesets of kind merge_branch only.",
			Blocks: map[string]schema.Block{
				"required_check": schema.ListNestedBlock{
					Description: "A check run that must pass. Repeat the block for each required check.",
					NestedObject: schema.NestedBlockObject{
						Attributes: map[string]schema.Attribute{
							"name": schema.StringAttribute{
								Required:    true,
								Description: "Check run name as reported to Origin.",
							},
							"app_id": schema.StringAttribute{
								Optional:    true,
								Description: "Origin app ID (app_...) that must report the check. Omit it to accept the check from any app.",
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
			Description: "Push rule: require updates to matching refs to go through a pull request. Takes no arguments; write `block_direct_updates {}`. Rulesets of kind push_branch, push_tag, or push_repository only.",
		},
		originRuleTypeRequiredLinearHistory: schema.SingleNestedBlock{
			Description: "Push rule: block merge commits so matching refs keep a linear history. Takes no arguments; write `required_linear_history {}`. Rulesets of kind push_branch, push_tag, or push_repository only.",
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
	if m.RequiredLinearHistory != nil {
		set = append(set, originRuleTypeRequiredLinearHistory)
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
		if !count.IsNull() && !count.IsUnknown() && count.ValueInt64() < 0 {
			return fmt.Errorf("%s.pull_request.required_approving_review_count must not be negative", name)
		}
	case originRuleTypeRequireStatusChecks:
		for i, check := range m.RequireStatusChecks.RequiredChecks {
			prefix := fmt.Sprintf("%s.require_status_checks.required_check[%d]", name, i)
			if err := requireNonEmptyUnpadded(check.Name, prefix+".name"); err != nil {
				return err
			}
			if !check.AppID.IsNull() {
				if err := requireNonEmptyUnpadded(check.AppID, prefix+".app_id"); err != nil {
					return err
				}
			}
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
		params := originPullRequestParameters{}
		if count := m.PullRequest.RequiredApprovingReviewCount; !count.IsNull() && !count.IsUnknown() {
			value := count.ValueInt64()
			params.RequiredApprovingReviewCount = &value
		}
		if params.RequiredApprovingReviewCount == nil {
			return nil, nil
		}
		wire = params
	case originRuleTypeRequireStatusChecks:
		params := originRequireStatusChecksParameters{
			RequiredChecks: make([]originRequiredCheckParameters, 0, len(m.RequireStatusChecks.RequiredChecks)),
		}
		for i, check := range m.RequireStatusChecks.RequiredChecks {
			if check.Name.IsUnknown() || check.AppID.IsUnknown() {
				return nil, fmt.Errorf("require_status_checks.required_check[%d] is incomplete", i)
			}
			params.RequiredChecks = append(params.RequiredChecks, originRequiredCheckParameters{
				Name:  check.Name.ValueString(),
				AppID: check.AppID.ValueString(),
			})
		}
		wire = params
	default:
		return nil, nil
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encoding %s parameters: %w", ruleType, err)
	}
	return json.RawMessage(encoded), nil
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
		block := &originPullRequestRuleModel{RequiredApprovingReviewCount: types.Int64Null()}
		if params.RequiredApprovingReviewCount != nil {
			block.RequiredApprovingReviewCount = types.Int64Value(*params.RequiredApprovingReviewCount)
		}
		model.PullRequest = block
	case originRuleTypeRequireStatusChecks:
		var params originRequireStatusChecksParameters
		if err := json.Unmarshal(raw, &params); err != nil {
			return originRepoRulesetRuleModel{}, fmt.Errorf("rule %q parameters: %w", rule.ID, err)
		}
		block := &originRequireStatusChecksRuleModel{
			RequiredChecks: make([]originRequiredCheckModel, 0, len(params.RequiredChecks)),
		}
		for _, check := range params.RequiredChecks {
			appID := types.StringNull()
			if check.AppID != "" {
				appID = types.StringValue(check.AppID)
			}
			block.RequiredChecks = append(block.RequiredChecks, originRequiredCheckModel{
				Name:  types.StringValue(check.Name),
				AppID: appID,
			})
		}
		model.RequireStatusChecks = block
	case originRuleTypeRequireBranchUpToDate:
		model.RequireBranchUpToDate = &originEmptyRuleModel{}
	case originRuleTypeDeletion:
		model.Deletion = &originEmptyRuleModel{}
	case originRuleTypeNonFastForward:
		model.NonFastForward = &originEmptyRuleModel{}
	case originRuleTypeBlockDirectUpdates:
		model.BlockDirectUpdates = &originEmptyRuleModel{}
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
		if want.PullRequest.RequiredApprovingReviewCount.IsNull() {
			block.RequiredApprovingReviewCount = types.Int64Null()
		} else if block.RequiredApprovingReviewCount.IsNull() {
			block.RequiredApprovingReviewCount = want.PullRequest.RequiredApprovingReviewCount
		}
		merged.PullRequest = &block
	}
	return merged, nil
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
