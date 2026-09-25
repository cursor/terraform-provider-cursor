package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type originInboundIPAllowlist struct {
	Enabled bool                            `json:"enabled"`
	Entries []originInboundIPAllowlistEntry `json:"entries"`
}

type originInboundIPAllowlistEntry struct {
	ID          string `json:"id"`
	CIDR        string `json:"cidr"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"createdAt"`
}

type originInboundIPAllowlistUpdate struct {
	Enabled bool `json:"enabled"`
}

type originInboundIPAllowlistEntryAdd struct {
	CIDR        string `json:"cidr"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
}

type originInboundIPAllowlistEntryPatch struct {
	CIDR        *string `json:"cidr,omitempty"`
	Description *string `json:"description,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
}

func (p originInboundIPAllowlistEntryPatch) empty() bool {
	return p.CIDR == nil && p.Description == nil && p.Enabled == nil
}

func originInboundIPAllowlistPath(namespace string) string {
	return "/namespaces/" + url.PathEscape(namespace) + "/inbound-ip-allowlist"
}

func originInboundIPAllowlistEntryPath(namespace, id string) string {
	return originInboundIPAllowlistPath(namespace) + "/entries/" + url.PathEscape(id)
}

func (c *apiClient) getOriginInboundIPAllowlist(ctx context.Context, namespace string) (*originInboundIPAllowlist, error) {
	body, err := c.originDo(ctx, http.MethodGet, originInboundIPAllowlistPath(namespace), nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return decodeOriginInboundIPAllowlist(body)
}

func (c *apiClient) setOriginInboundIPAllowlistEnabled(ctx context.Context, namespace string, enabled bool) (*originInboundIPAllowlist, error) {
	body, err := c.originDo(ctx, http.MethodPatch, originInboundIPAllowlistPath(namespace), originInboundIPAllowlistUpdate{Enabled: enabled}, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return decodeOriginInboundIPAllowlist(body)
}

func (c *apiClient) addOriginInboundIPAllowlistEntry(ctx context.Context, namespace string, add originInboundIPAllowlistEntryAdd) (*originInboundIPAllowlistEntry, error) {
	body, err := c.originDo(ctx, http.MethodPost, originInboundIPAllowlistPath(namespace)+"/entries", add, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	entry, err := decodeOriginInboundIPAllowlistEntry(body, "")
	if err != nil {
		return nil, err
	}
	if entry.CIDR != strings.TrimSpace(add.CIDR) {
		return nil, fmt.Errorf("Origin API returned inbound IP allowlist entry %s with cidr %q, want %q", entry.ID, entry.CIDR, strings.TrimSpace(add.CIDR))
	}
	return entry, nil
}

func (c *apiClient) getOriginInboundIPAllowlistEntry(ctx context.Context, namespace, id string) (*originInboundIPAllowlistEntry, error) {
	if id == "" {
		return nil, fmt.Errorf("inbound IP allowlist entry id is required")
	}
	body, err := c.originDo(ctx, http.MethodGet, originInboundIPAllowlistEntryPath(namespace, id), nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return decodeOriginInboundIPAllowlistEntry(body, id)
}

// A 404 on the entry path does not tell a removed entry from a hidden namespace, so the list decides.
func (c *apiClient) lookupOriginInboundIPAllowlistEntry(ctx context.Context, namespace, id string) (*originInboundIPAllowlistEntry, error) {
	entry, err := c.getOriginInboundIPAllowlistEntry(ctx, namespace, id)
	if err == nil || !isOriginNotFound(err) {
		return entry, err
	}
	list, err := c.getOriginInboundIPAllowlist(ctx, namespace)
	if err != nil {
		return nil, err
	}
	for i := range list.Entries {
		if list.Entries[i].ID == id {
			return &list.Entries[i], nil
		}
	}
	return nil, nil
}

func (c *apiClient) updateOriginInboundIPAllowlistEntry(ctx context.Context, namespace, id string, patch originInboundIPAllowlistEntryPatch) (*originInboundIPAllowlistEntry, error) {
	if id == "" {
		return nil, fmt.Errorf("inbound IP allowlist entry id is required")
	}
	body, err := c.originDo(ctx, http.MethodPatch, originInboundIPAllowlistEntryPath(namespace, id), patch, http.StatusOK)
	if err != nil {
		return nil, err
	}
	entry, err := decodeOriginInboundIPAllowlistEntry(body, id)
	if err != nil {
		return nil, err
	}
	if patch.CIDR != nil && entry.CIDR != strings.TrimSpace(*patch.CIDR) {
		return nil, fmt.Errorf("Origin API returned inbound IP allowlist entry %s with cidr %q, want %q", entry.ID, entry.CIDR, strings.TrimSpace(*patch.CIDR))
	}
	return entry, nil
}

func (c *apiClient) deleteOriginInboundIPAllowlistEntry(ctx context.Context, namespace, id string) error {
	if id == "" {
		return fmt.Errorf("inbound IP allowlist entry id is required")
	}
	_, err := c.originDo(ctx, http.MethodDelete, originInboundIPAllowlistEntryPath(namespace, id), nil, http.StatusOK, http.StatusNoContent)
	return err
}

func decodeOriginInboundIPAllowlist(body []byte) (*originInboundIPAllowlist, error) {
	var list originInboundIPAllowlist
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("decoding Origin inbound IP allowlist: %w", err)
	}
	for _, entry := range list.Entries {
		if err := checkOriginInboundIPAllowlistEntry(entry); err != nil {
			return nil, err
		}
	}
	if list.Entries == nil {
		list.Entries = []originInboundIPAllowlistEntry{}
	}
	return &list, nil
}

func decodeOriginInboundIPAllowlistEntry(body []byte, wantID string) (*originInboundIPAllowlistEntry, error) {
	var entry originInboundIPAllowlistEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		return nil, fmt.Errorf("decoding Origin inbound IP allowlist entry: %w", err)
	}
	if err := checkOriginInboundIPAllowlistEntry(entry); err != nil {
		return nil, err
	}
	if wantID != "" && entry.ID != wantID {
		return nil, fmt.Errorf("Origin API returned inbound IP allowlist entry %s for %s", entry.ID, wantID)
	}
	return &entry, nil
}

func checkOriginInboundIPAllowlistEntry(entry originInboundIPAllowlistEntry) error {
	if strings.TrimSpace(entry.ID) == "" {
		return fmt.Errorf("Origin API returned an inbound IP allowlist entry without an id")
	}
	if strings.TrimSpace(entry.CIDR) == "" {
		return fmt.Errorf("Origin API returned inbound IP allowlist entry %s without a cidr", entry.ID)
	}
	return nil
}
