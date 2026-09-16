package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	originGrantKindUser      = "user"
	originGrantKindGroup     = "group"
	originGrantKindTeamGroup = "team_group"

	originTeamGroupMembers = "members"
	originTeamGroupAdmins  = "admins"

	originPermissionRead        = "read"
	originPermissionContributor = "contributor"
	originPermissionWrite       = "write"
	originPermissionAdmin       = "admin"
	originPermissionCustom      = "custom"

	// Namespace grants carry proto enum names (PERMISSION_READ); repository grants carry bare names (read).
	originOwnerPermissionPrefix = "PERMISSION_"

	originGrantPageSize = 100
	maxOriginGrantPages = 100
)

// Exactly one principal is set; a delete request is the same shape without permission.
type originGrant struct {
	User       *originGrantUser      `json:"user,omitempty"`
	Group      *originGrantGroup     `json:"group,omitempty"`
	TeamGroup  *originGrantTeamGroup `json:"teamGroup,omitempty"`
	Permission string                `json:"permission,omitempty"`
}

type originGrantUser struct {
	ID string `json:"id"`
}

type originGrantGroup struct {
	ID string `json:"id"`
}

type originGrantTeamGroup struct {
	Kind string `json:"kind"`
}

type originGrantList struct {
	Grants []originGrant `json:"grants"`
}

// originGrantKey is the principal half of a grant's principal × resource identity.
type originGrantKey struct {
	kind  string
	value string
}

func (k originGrantKey) String() string {
	return k.kind + ":" + k.value
}

func (k originGrantKey) wire() originGrant {
	switch k.kind {
	case originGrantKindUser:
		return originGrant{User: &originGrantUser{ID: k.value}}
	case originGrantKindGroup:
		return originGrant{Group: &originGrantGroup{ID: k.value}}
	default:
		return originGrant{TeamGroup: &originGrantTeamGroup{Kind: k.value}}
	}
}

func originGrantKeyFromWire(grant originGrant) (originGrantKey, error) {
	set := 0
	var key originGrantKey
	if grant.User != nil {
		set++
		key = originGrantKey{kind: originGrantKindUser, value: grant.User.ID}
	}
	if grant.Group != nil {
		set++
		key = originGrantKey{kind: originGrantKindGroup, value: grant.Group.ID}
	}
	if grant.TeamGroup != nil {
		set++
		key = originGrantKey{kind: originGrantKindTeamGroup, value: grant.TeamGroup.Kind}
	}
	if set != 1 {
		return originGrantKey{}, fmt.Errorf("Origin API returned a grant without exactly one principal")
	}
	if strings.TrimSpace(key.value) == "" {
		return originGrantKey{}, fmt.Errorf("Origin API returned a %s grant without an identifier", key.kind)
	}
	return key, nil
}

func originRepoGrantsPath(owner, repo string) string {
	return originRepoPath(owner, repo) + "/grants"
}

func originOwnerGrantsPath(owner string) string {
	return "/owners/" + url.PathEscape(owner) + "/grants"
}

func (c *apiClient) listOriginGrants(ctx context.Context, collection string) ([]originGrant, error) {
	var all []originGrant
	err := c.eachOriginGrantPage(ctx, collection, func(page []originGrant) bool {
		all = append(all, page...)
		return false
	})
	if err != nil {
		return nil, err
	}
	if all == nil {
		all = []originGrant{}
	}
	return all, nil
}

// Returns nil when the principal has no direct grant.
func (c *apiClient) findOriginGrant(ctx context.Context, collection string, key originGrantKey) (*originGrant, error) {
	var found *originGrant
	err := c.eachOriginGrantPage(ctx, collection, func(page []originGrant) bool {
		for i := range page {
			got, err := originGrantKeyFromWire(page[i])
			if err != nil {
				continue
			}
			if got == key {
				grant := page[i]
				found = &grant
				return true
			}
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// Paging stops at the first empty or short page.
func (c *apiClient) eachOriginGrantPage(ctx context.Context, collection string, visit func([]originGrant) (stop bool)) error {
	for page := 1; page <= maxOriginGrantPages; page++ {
		query := "?page=" + strconv.Itoa(page) + "&pageSize=" + strconv.Itoa(originGrantPageSize)
		body, err := c.originDo(ctx, http.MethodGet, collection+query, nil, http.StatusOK)
		if err != nil {
			return err
		}
		var list originGrantList
		if err := json.Unmarshal(body, &list); err != nil {
			return fmt.Errorf("decoding Origin grants: %w", err)
		}
		if len(list.Grants) == 0 {
			return nil
		}
		if visit(list.Grants) || len(list.Grants) < originGrantPageSize {
			return nil
		}
	}
	return fmt.Errorf("Origin API returned more than %d pages of grants", maxOriginGrantPages)
}

func (c *apiClient) upsertOriginGrant(ctx context.Context, collection string, grant originGrant) (*originGrant, error) {
	if _, err := originGrantKeyFromWire(grant); err != nil {
		return nil, fmt.Errorf("grant must name exactly one principal")
	}
	if grant.Permission == "" {
		return nil, fmt.Errorf("grant permission is required")
	}
	body, err := c.originDo(ctx, http.MethodPost, collection, grant, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return decodeOriginGrant(body, grant)
}

func (c *apiClient) deleteOriginGrant(ctx context.Context, collection string, key originGrantKey) error {
	_, err := c.originDo(ctx, http.MethodDelete, collection, key.wire(), http.StatusOK, http.StatusNoContent)
	return err
}

// An empty acknowledgement falls back to the requested grant.
func decodeOriginGrant(body []byte, requested originGrant) (*originGrant, error) {
	if strings.TrimSpace(string(body)) == "" {
		return &requested, nil
	}
	var grant originGrant
	if err := json.Unmarshal(body, &grant); err != nil {
		return nil, fmt.Errorf("decoding Origin grant: %w", err)
	}
	if grant.User == nil && grant.Group == nil && grant.TeamGroup == nil {
		return &requested, nil
	}
	got, err := originGrantKeyFromWire(grant)
	if err != nil {
		return nil, err
	}
	want, err := originGrantKeyFromWire(requested)
	if err != nil {
		return nil, err
	}
	if got != want {
		return nil, fmt.Errorf("Origin API returned grant for %s, want %s", got, want)
	}
	if grant.Permission == "" {
		grant.Permission = requested.Permission
	}
	return &grant, nil
}

func ownerPermissionToWire(permission string) string {
	return originOwnerPermissionPrefix + strings.ToUpper(permission)
}

func ownerPermissionFromWire(permission string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(permission), originOwnerPermissionPrefix))
}

func repoPermissionToWire(permission string) string {
	return permission
}

func repoPermissionFromWire(permission string) string {
	return strings.ToLower(strings.TrimSpace(permission))
}

// Composite ID, for example acme/rocket:user:user_01... or acme:team_group:admins.
func originGrantID(resource string, key originGrantKey) string {
	return resource + ":" + key.String()
}

// Import IDs are resource:kind:value where kind is a stored principal kind or user_email / group_name to resolve.
func parseOriginGrantImportID(id string) (string, string, string, error) {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || strings.TrimSpace(id) != id {
		return "", "", "", fmt.Errorf("invalid import ID %q, expected resource:principal_kind:principal", id)
	}
	resource, kind, value := parts[0], parts[1], parts[2]
	switch kind {
	case originGrantKindUser, originGrantKindGroup, principalFamilyUserEmail, principalFamilyGroupName:
	case originGrantKindTeamGroup:
		if value != originTeamGroupMembers && value != originTeamGroupAdmins {
			return "", "", "", fmt.Errorf("invalid import ID %q, team_group must be %s or %s", id, originTeamGroupMembers, originTeamGroupAdmins)
		}
	default:
		return "", "", "", fmt.Errorf("invalid import ID %q, principal kind must be %s, %s, %s, %s, or %s", id, originGrantKindUser, originGrantKindGroup, originGrantKindTeamGroup, principalFamilyUserEmail, principalFamilyGroupName)
	}
	return resource, kind, value, nil
}

func splitOriginRepoResource(resource string) (string, string, error) {
	parts := strings.Split(resource, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repository %q, expected owner/repo", resource)
	}
	return parts[0], parts[1], nil
}
