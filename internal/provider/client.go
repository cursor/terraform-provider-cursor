package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	connect "connectrpc.com/connect"
	v1connect "github.com/cursor/terraform-provider-cursor/internal/proto/v1/v1connect"
)

const userAgentPrefix = "terraform-provider-cursor/"

type apiClient struct {
	automations v1connect.AutomationsServiceClient
}

func newAPIClient(endpoint string, token string, version string) (*apiClient, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}

	// If the token is a raw API key, exchange it for a session token before
	// building the Connect client.
	bearerToken, err := resolveToken(httpClient, endpoint, token)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange API key for session token: %w", err)
	}

	interceptors := []connect.Interceptor{
		userAgentInterceptor(version),
	}
	authHeader := formatAuthHeader(bearerToken)
	if authHeader != "" {
		interceptors = append(interceptors, authInterceptor(authHeader))
	}

	client := v1connect.NewAutomationsServiceClient(
		httpClient,
		endpoint,
		connect.WithInterceptors(interceptors...),
	)

	return &apiClient{
		automations: client,
	}, nil
}

// isAPIKey returns true when the token looks like a raw Cursor user API key
// that must be exchanged before use.
func isAPIKey(token string) bool {
	trimmed := strings.TrimSpace(token)
	return strings.HasPrefix(trimmed, "key_") || strings.HasPrefix(trimmed, "crsr_")
}

// exchangeResponse is the JSON body returned by /auth/exchange_user_api_key.
type exchangeResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// resolveToken returns a bearer token suitable for the Authorization header.
// If the supplied token is a raw API key (key_ prefix), it is exchanged via
// the /auth/exchange_user_api_key endpoint first. Otherwise the token is
// returned as-is.
func resolveToken(httpClient *http.Client, endpoint string, token string) (string, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return trimmed, nil
	}
	if !isAPIKey(trimmed) {
		return trimmed, nil
	}

	url := strings.TrimRight(endpoint, "/") + "/auth/exchange_user_api_key"

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", fmt.Errorf("building exchange request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+trimmed)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling exchange endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading exchange response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("exchange endpoint returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result exchangeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decoding exchange response: %w", err)
	}
	accessToken := strings.TrimSpace(result.AccessToken)
	if accessToken == "" {
		return "", fmt.Errorf("exchange endpoint returned empty access token")
	}

	return accessToken, nil
}

func formatAuthHeader(token string) string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "bearer ") {
		return trimmed
	}
	return "Bearer " + trimmed
}

func authInterceptor(value string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", value)
			return next(ctx, req)
		}
	})
}

func userAgentInterceptor(version string) connect.Interceptor {
	agent := userAgentPrefix + strings.TrimSpace(version)
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("User-Agent", agent)
			return next(ctx, req)
		}
	})
}
