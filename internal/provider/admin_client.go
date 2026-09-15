package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Cursor Admin APIs (Team and Organization) share one host and use HTTP Basic auth with the key as username.
const defaultAdminAPIBase = "https://api.cursor.com"

const (
	adminTeamMembersPath = "/teams/members"
	adminOrgGroupsPath   = "/organizations/groups"

	adminPageSize = 100
	maxAdminPages = 100

	originUserIDPrefix  = "user_"
	originGroupIDPrefix = "grp_"
)

type adminAPIError struct {
	API        string
	StatusCode int
	Message    string
}

func (e *adminAPIError) Error() string {
	msg := fmt.Sprintf("%s returned HTTP %d", e.API, e.StatusCode)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

type adminPagination struct {
	HasNextPage *bool `json:"hasNextPage"`
}

type adminTeamMember struct {
	ID       string `json:"id"`
	PublicID string `json:"publicId"`
	Email    string `json:"email"`
	Name     string `json:"name"`
}

type adminTeamMembersResponse struct {
	TeamMembers []adminTeamMember `json:"teamMembers"`
	Pagination  *adminPagination  `json:"pagination"`
}

type adminGroup struct {
	ID       string `json:"id"`
	PublicID string `json:"publicId"`
	Name     string `json:"name"`
}

type adminGroupsResponse struct {
	Groups     []adminGroup     `json:"groups"`
	Pagination *adminPagination `json:"pagination"`
}

// The member's Origin user TypeID, whichever field carries it.
func (m adminTeamMember) userID() string {
	for _, candidate := range []string{m.PublicID, m.ID} {
		if strings.HasPrefix(candidate, originUserIDPrefix) {
			return candidate
		}
	}
	return ""
}

// resolveTeamMemberID maps an email to a user_ ID through the Team Admin API.
func (c *apiClient) resolveTeamMemberID(ctx context.Context, email string) (string, error) {
	if c == nil || c.teamAPIKey == "" {
		return "", fmt.Errorf("team_api_key is required to resolve user_email; set the provider team_api_key attribute or %s", envTeamAPIKey)
	}
	want := strings.TrimSpace(email)
	if want == "" {
		return "", fmt.Errorf("user_email is required")
	}
	var matches []adminTeamMember
	err := c.eachAdminPage(ctx, "Team Admin API", c.teamAPIKey, adminTeamMembersPath, func(body []byte) (int, *adminPagination, error) {
		var page adminTeamMembersResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return 0, nil, fmt.Errorf("decoding team members: %w", err)
		}
		for _, member := range page.TeamMembers {
			if strings.EqualFold(strings.TrimSpace(member.Email), want) {
				matches = append(matches, member)
			}
		}
		return len(page.TeamMembers), page.Pagination, nil
	})
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no team member has email %q", email)
	case 1:
	default:
		return "", fmt.Errorf("%d team members have email %q; the Team Admin API must return exactly one", len(matches), email)
	}
	id := matches[0].userID()
	if id == "" {
		return "", fmt.Errorf("team member %q has no %s identifier; the Team Admin API must return the member's user ID", email, originUserIDPrefix)
	}
	return id, nil
}

// resolveOrganizationGroupID maps an exact group name to its publicId (grp_) via the list endpoint's ?name= filter.
func (c *apiClient) resolveOrganizationGroupID(ctx context.Context, name string) (string, error) {
	if c == nil || c.orgAPIKey == "" {
		return "", fmt.Errorf("organization_api_key is required to resolve group_name; set the provider organization_api_key attribute or %s", envOrgAPIKey)
	}
	want := strings.TrimSpace(name)
	if want == "" {
		return "", fmt.Errorf("group_name is required")
	}
	query := url.Values{"name": {want}}
	body, err := c.adminGet(ctx, "Organization Admin API", c.orgAPIKey, adminOrgGroupsPath+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var list adminGroupsResponse
	if err := json.Unmarshal(body, &list); err != nil {
		return "", fmt.Errorf("decoding organization groups: %w", err)
	}
	switch len(list.Groups) {
	case 0:
		return "", fmt.Errorf("no organization group is named %q (names match exactly, including case)", name)
	case 1:
	default:
		return "", fmt.Errorf("Organization Admin API returned %d groups for name %q; expected the exact-name filter to return at most one", len(list.Groups), name)
	}
	group := list.Groups[0]
	if strings.TrimSpace(group.Name) != want {
		return "", fmt.Errorf("Organization Admin API returned group %q for name %q; the ?name= filter was not applied", group.Name, name)
	}
	publicID := strings.TrimSpace(group.PublicID)
	if publicID == "" {
		return "", fmt.Errorf("organization group %q has no publicId; grants require the group's %s public ID, which the Organization Admin API returns as publicId", name, originGroupIDPrefix)
	}
	if !strings.HasPrefix(publicID, originGroupIDPrefix) {
		return "", fmt.Errorf("organization group %q publicId %q does not start with %s", name, publicID, originGroupIDPrefix)
	}
	return publicID, nil
}

// Stops at hasNextPage=false when the API reports it, else at the first empty or short page.
func (c *apiClient) eachAdminPage(ctx context.Context, api, key, path string, visit func(body []byte) (int, *adminPagination, error)) error {
	for page := 1; page <= maxAdminPages; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(adminPageSize)}}
		body, err := c.adminGet(ctx, api, key, path+"?"+query.Encode())
		if err != nil {
			return err
		}
		items, pagination, err := visit(body)
		if err != nil {
			return err
		}
		if pagination != nil && pagination.HasNextPage != nil {
			if !*pagination.HasNextPage {
				return nil
			}
			continue
		}
		if items < adminPageSize {
			return nil
		}
	}
	return fmt.Errorf("%s returned more than %d pages", api, maxAdminPages)
}

func (c *apiClient) adminGet(ctx context.Context, api, key, path string) ([]byte, error) {
	if c == nil || c.httpClient == nil {
		return nil, fmt.Errorf("%s client is not configured", api)
	}
	base := strings.TrimRight(strings.TrimSpace(c.adminBase), "/")
	if base == "" {
		base = defaultAdminAPIBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("building %s request: %w", api, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", basicAuthHeader(key))
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", api, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading %s response: %w", api, err)
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &adminAPIError{API: api, StatusCode: resp.StatusCode, Message: adminErrorMessage(body)}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			apiErr.Message = strings.TrimSpace("API key was rejected; check it is the admin key for this " + strings.ToLower(strings.TrimSuffix(api, " Admin API")) + ". " + apiErr.Message)
		}
		return nil, apiErr
	}
	return body, nil
}

func adminErrorMessage(body []byte) string {
	var payload struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if msg := strings.TrimSpace(payload.Message); msg != "" {
			return msg
		}
		if msg := strings.TrimSpace(payload.Error); msg != "" {
			return msg
		}
	}
	return originErrorMessage(body)
}

// Basic auth with the API key as username and an empty password.
func basicAuthHeader(key string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(key)+":"))
}
