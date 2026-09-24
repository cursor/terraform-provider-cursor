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
	originSSHFingerprintPrefix    = "SHA256:"
	originSSHCertificateKeySuffix = "-cert-v01@openssh.com"
	maxOriginSSHAuthorityNameLen  = 255
)

// publicKey comes back as `<key_type> <base64>`, without the comment the caller may have sent.
type originSSHCertificateAuthority struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
	CreatedAt   string `json:"createdAt"`
}

type originSSHCertificateAuthorityList struct {
	CertificateAuthorities []originSSHCertificateAuthority `json:"certificateAuthorities"`
	RequireCertificates    bool                            `json:"requireCertificates"`
}

type originSSHCertificateAuthorityWrite struct {
	PublicKey string `json:"publicKey"`
	Name      string `json:"name"`
}

type originSSHCertificateRequirement struct {
	RequireCertificates bool `json:"requireCertificates"`
}

func originSSHCertificateAuthoritiesPath(owner string) string {
	return "/owners/" + url.PathEscape(owner) + "/ssh-certificate-authorities"
}

func (c *apiClient) listOriginSSHCertificateAuthorities(ctx context.Context, owner string) (*originSSHCertificateAuthorityList, error) {
	body, err := c.originDo(ctx, http.MethodGet, originSSHCertificateAuthoritiesPath(owner), nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	var list originSSHCertificateAuthorityList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("decoding Origin SSH certificate authorities: %w", err)
	}
	for _, authority := range list.CertificateAuthorities {
		if err := checkOriginSSHCertificateAuthority(authority); err != nil {
			return nil, err
		}
	}
	if list.CertificateAuthorities == nil {
		list.CertificateAuthorities = []originSSHCertificateAuthority{}
	}
	return &list, nil
}

func (c *apiClient) addOriginSSHCertificateAuthority(ctx context.Context, owner string, write originSSHCertificateAuthorityWrite) (*originSSHCertificateAuthority, error) {
	body, err := c.originDo(ctx, http.MethodPost, originSSHCertificateAuthoritiesPath(owner), write, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	var authority originSSHCertificateAuthority
	if err := json.Unmarshal(body, &authority); err != nil {
		return nil, fmt.Errorf("decoding Origin SSH certificate authority: %w", err)
	}
	if err := checkOriginSSHCertificateAuthority(authority); err != nil {
		return nil, err
	}
	if normalizeSSHPublicKey(authority.PublicKey) != normalizeSSHPublicKey(write.PublicKey) {
		return nil, fmt.Errorf("Origin API returned authority %s for a different public key", authority.ID)
	}
	return &authority, nil
}

func (c *apiClient) deleteOriginSSHCertificateAuthority(ctx context.Context, owner, id string) error {
	if id == "" {
		return fmt.Errorf("certificate authority id is required")
	}
	_, err := c.originDo(ctx, http.MethodDelete, originSSHCertificateAuthoritiesPath(owner)+"/"+url.PathEscape(id), nil, http.StatusOK, http.StatusNoContent)
	return err
}

func (c *apiClient) setOriginSSHCertificateRequirement(ctx context.Context, owner string, require bool) (bool, error) {
	body, err := c.originDo(ctx, http.MethodPost, originSSHCertificateAuthoritiesPath(owner)+":setRequirement", originSSHCertificateRequirement{RequireCertificates: require}, http.StatusOK)
	if err != nil {
		return false, err
	}
	var requirement originSSHCertificateRequirement
	if err := json.Unmarshal(body, &requirement); err != nil {
		return false, fmt.Errorf("decoding Origin SSH certificate requirement: %w", err)
	}
	return requirement.RequireCertificates, nil
}

func checkOriginSSHCertificateAuthority(authority originSSHCertificateAuthority) error {
	if strings.TrimSpace(authority.ID) == "" {
		return fmt.Errorf("Origin API returned an SSH certificate authority without an id")
	}
	if strings.TrimSpace(authority.PublicKey) == "" || strings.TrimSpace(authority.Fingerprint) == "" {
		return fmt.Errorf("Origin API returned SSH certificate authority %s without a public key or fingerprint", authority.ID)
	}
	return nil
}

// Returns nil when no listed authority has the id.
func findOriginSSHCertificateAuthority(authorities []originSSHCertificateAuthority, id string) *originSSHCertificateAuthority {
	for i := range authorities {
		if authorities[i].ID == id {
			return &authorities[i]
		}
	}
	return nil
}

// normalizeSSHPublicKey keeps the `<key_type> <base64>` part of an authorized_keys line; comment and whitespace are not part of the key.
func normalizeSSHPublicKey(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return strings.TrimSpace(line)
	}
	return fields[0] + " " + fields[1]
}
