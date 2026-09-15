package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	originRulesetKindMergeBranch    = "merge_branch"
	originRulesetKindPushBranch     = "push_branch"
	originRulesetKindPushTag        = "push_tag"
	originRulesetKindPushRepository = "push_repository"

	originRulesetEnforcementActive   = "active"
	originRulesetEnforcementEvaluate = "evaluate"
	originRulesetEnforcementDisabled = "disabled"

	originBypassModeAlways          = "always"
	originBypassModePullRequestOnly = "pull_request_only"

	originRoleNamespaceAdmin  = "namespace_admin"
	originRoleRepositoryAdmin = "repository_admin"
	originRoleRepositoryWrite = "repository_write"

	maxOriginIncludedRefs = 64
	maxOriginRules        = 20
	maxOriginBypassActors = 15
)

type originRuleset struct {
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	Description      string                     `json:"description"`
	Enforcement      string                     `json:"enforcement"`
	Kind             string                     `json:"kind"`
	IncludedRefNames []string                   `json:"includedRefNames"`
	ExcludedRefNames []string                   `json:"excludedRefNames"`
	Rules            []originRulesetRule        `json:"rules"`
	BypassActors     []originRulesetBypassActor `json:"bypassActors"`
}

type originRulesetWrite struct {
	Name             string                          `json:"name"`
	Description      string                          `json:"description"`
	Enforcement      string                          `json:"enforcement"`
	Kind             string                          `json:"kind"`
	IncludedRefNames []string                        `json:"includedRefNames"`
	ExcludedRefNames []string                        `json:"excludedRefNames"`
	Rules            []originRulesetRuleInput        `json:"rules"`
	BypassActors     []originRulesetBypassActorInput `json:"bypassActors"`
}

type originRulesetRule struct {
	ID         string          `json:"id"`
	RuleType   string          `json:"ruleType"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

type originRulesetRuleInput struct {
	RuleType   string          `json:"ruleType"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

type originRulesetBypassActor struct {
	ID         string                         `json:"id"`
	BypassMode string                         `json:"bypassMode"`
	User       *originRulesetUserBypass       `json:"user,omitempty"`
	Team       *originRulesetTeamBypass       `json:"team,omitempty"`
	App        *originRulesetAppBypass        `json:"app,omitempty"`
	OriginRole *originRulesetOriginRoleBypass `json:"originRole,omitempty"`
}

type originRulesetBypassActorInput struct {
	BypassMode string                         `json:"bypassMode"`
	User       *originRulesetUserBypass       `json:"user,omitempty"`
	Team       *originRulesetTeamBypass       `json:"team,omitempty"`
	App        *originRulesetAppBypass        `json:"app,omitempty"`
	OriginRole *originRulesetOriginRoleBypass `json:"originRole,omitempty"`
}

type originRulesetUserBypass struct {
	ID string `json:"id"`
}

type originRulesetTeamBypass struct {
	OrganizationPublicID string `json:"organizationPublicId"`
	GroupPublicID        string `json:"groupPublicId"`
}

type originRulesetAppBypass struct {
	ID string `json:"id"`
}

type originRulesetOriginRoleBypass struct {
	Role string `json:"role"`
}

func (c *apiClient) createOriginRuleset(ctx context.Context, owner, repo string, body originRulesetWrite) (*originRuleset, error) {
	payload, err := c.originDo(ctx, http.MethodPost, originRulesetsPath(owner, repo), body, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return decodeOriginRuleset(payload)
}

func (c *apiClient) getOriginRuleset(ctx context.Context, owner, repo, rulesetID string) (*originRuleset, error) {
	if err := requireRulesetID(rulesetID); err != nil {
		return nil, err
	}
	payload, err := c.originDo(ctx, http.MethodGet, originRulesetPath(owner, repo, rulesetID), nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return decodeOriginRuleset(payload)
}

func (c *apiClient) updateOriginRuleset(ctx context.Context, owner, repo, rulesetID string, body originRulesetWrite) (*originRuleset, error) {
	if err := requireRulesetID(rulesetID); err != nil {
		return nil, err
	}
	payload, err := c.originDo(ctx, http.MethodPut, originRulesetPath(owner, repo, rulesetID), body, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return decodeOriginRuleset(payload)
}

func (c *apiClient) deleteOriginRuleset(ctx context.Context, owner, repo, rulesetID string) error {
	if err := requireRulesetID(rulesetID); err != nil {
		return err
	}
	_, err := c.originDo(ctx, http.MethodDelete, originRulesetPath(owner, repo, rulesetID), nil, http.StatusOK, http.StatusNoContent)
	return err
}

func requireRulesetID(rulesetID string) error {
	if rulesetID == "" {
		return fmt.Errorf("ruleset id is required")
	}
	if strings.TrimSpace(rulesetID) != rulesetID {
		return fmt.Errorf("ruleset id must not have leading or trailing spaces")
	}
	return nil
}

func originRulesetsPath(owner, repo string) string {
	return originRepoPath(owner, repo) + "/rulesets"
}

func originRulesetPath(owner, repo, rulesetID string) string {
	return originRulesetsPath(owner, repo) + "/" + url.PathEscape(rulesetID)
}

func decodeOriginRuleset(body []byte) (*originRuleset, error) {
	var ruleset originRuleset
	if err := json.Unmarshal(body, &ruleset); err != nil {
		return nil, fmt.Errorf("decoding Origin ruleset: %w", err)
	}
	if ruleset.ID == "" {
		return nil, fmt.Errorf("Origin API returned a ruleset without an id")
	}
	if ruleset.Name == "" {
		return nil, fmt.Errorf("Origin API returned a ruleset without a name")
	}
	return &ruleset, nil
}
