package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// originRepoIDOwner is the sentinel owner slug that addresses a repository
	// by its stable ID: GET /v1/origin/repos/_/{id}.
	originRepoIDOwner = "_"

	maxOriginErrorBody = 512
)

type originRepo struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	FullName            string        `json:"fullName"`
	Owner               *originOwner  `json:"owner"`
	DefaultBranch       string        `json:"defaultBranch"`
	CreatedAt           string        `json:"createdAt"`
	UpdatedAt           string        `json:"updatedAt"`
	PushedAt            string        `json:"pushedAt"`
	CloneURL            string        `json:"cloneUrl"`
	Mirror              *originMirror `json:"mirror"`
	Visibility          string        `json:"visibility"`
	AllowMergeCommit    *bool         `json:"allowMergeCommit"`
	AllowSquashMerge    *bool         `json:"allowSquashMerge"`
	DeleteBranchOnMerge *bool         `json:"deleteBranchOnMerge"`
}

type originOwner struct {
	Slug string `json:"slug"`
	ID   string `json:"id"`
	Type string `json:"type"`
}

type originMirror struct {
	Source   string `json:"source"`
	SourceID string `json:"sourceId"`
	Status   string `json:"status"`
}

type originStatusError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type originAPIError struct {
	StatusCode int
	Message    string
	RequestID  string
}

func (e *originAPIError) Error() string {
	msg := fmt.Sprintf("Origin API returned HTTP %d", e.StatusCode)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.RequestID != "" {
		msg += " (request id " + e.RequestID + ")"
	}
	return msg
}

// getOriginRepo reads one repository. Pass originRepoIDOwner and the repository
// ID to address a repo by its stable ID.
func (c *apiClient) getOriginRepo(ctx context.Context, owner, name string) (*originRepo, error) {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" || name == "" {
		return nil, fmt.Errorf("owner and name are required")
	}
	body, err := c.originDo(ctx, http.MethodGet, originRepoPath(owner, name), nil, http.StatusOK)
	if err != nil {
		return nil, err
	}

	var repo originRepo
	if err := json.Unmarshal(body, &repo); err != nil {
		return nil, fmt.Errorf("decoding Origin repository: %w", err)
	}
	if strings.TrimSpace(repo.ID) == "" || strings.TrimSpace(repo.Name) == "" {
		return nil, fmt.Errorf("Origin API returned a repository without an id or name")
	}
	if repo.Owner == nil || strings.TrimSpace(repo.Owner.Slug) == "" {
		return nil, fmt.Errorf("Origin API returned a repository without an owner")
	}
	return &repo, nil
}

func originRepoPath(owner, name string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func (c *apiClient) originDo(ctx context.Context, method, path string, payload any, success ...int) ([]byte, error) {
	if c == nil || c.httpClient == nil {
		return nil, fmt.Errorf("Origin API client is not configured")
	}
	base := strings.TrimRight(strings.TrimSpace(c.originBase), "/")
	if base == "" {
		base = defaultOriginAPIBase
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	var bodyReader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encoding Origin request: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("building Origin request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.authHeader != "" {
		req.Header.Set("Authorization", c.authHeader)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Origin API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading Origin API response: %w", err)
	}
	for _, code := range success {
		if resp.StatusCode == code {
			return body, nil
		}
	}
	apiErr := &originAPIError{
		StatusCode: resp.StatusCode,
		RequestID:  strings.TrimSpace(resp.Header.Get("X-Request-Id")),
	}
	if resp.StatusCode == http.StatusNotFound {
		apiErr.Message = originNotFoundMessage(body)
		return nil, apiErr
	}
	apiErr.Message = originErrorMessage(body)
	return nil, apiErr
}

func originNotFoundMessage(body []byte) string {
	detail := originErrorMessage(body)
	msg := "not found or not visible to this token; the Origin API does not distinguish a missing resource from one the caller cannot access"
	if detail == "" {
		return msg
	}
	return msg + ": " + detail
}

func originErrorMessage(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var status originStatusError
	if err := json.Unmarshal(body, &status); err == nil {
		if msg := strings.TrimSpace(status.Message); msg != "" {
			return msg
		}
	}
	if len(trimmed) > maxOriginErrorBody {
		return trimmed[:maxOriginErrorBody] + "..."
	}
	return trimmed
}
