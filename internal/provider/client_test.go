package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveToken_ExchangesAPIKey(t *testing.T) {
	const fakeAPIKey = "key_test123"
	const fakeAccessToken = "session_abc"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/exchange_user_api_key" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+fakeAPIKey {
			t.Errorf("expected Authorization: Bearer %s, got %s", fakeAPIKey, auth)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(exchangeResponse{
			AccessToken:  fakeAccessToken,
			RefreshToken: fakeAccessToken,
		})
	}))
	defer server.Close()

	httpClient := &http.Client{}
	got, err := resolveToken(httpClient, server.URL, fakeAPIKey)
	if err != nil {
		t.Fatalf("resolveToken() error: %v", err)
	}
	if got != fakeAccessToken {
		t.Errorf("resolveToken() = %q, want %q", got, fakeAccessToken)
	}
}

func TestResolveToken_PassthroughNonAPIKey(t *testing.T) {
	// A session token (no key_ prefix) should be returned as-is without
	// making any HTTP calls.
	httpClient := &http.Client{}
	token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.test"

	got, err := resolveToken(httpClient, "https://should-not-be-called.invalid", token)
	if err != nil {
		t.Fatalf("resolveToken() error: %v", err)
	}
	if got != token {
		t.Errorf("resolveToken() = %q, want %q", got, token)
	}
}

func TestResolveToken_EmptyToken(t *testing.T) {
	httpClient := &http.Client{}
	got, err := resolveToken(httpClient, "https://should-not-be-called.invalid", "")
	if err != nil {
		t.Fatalf("resolveToken() error: %v", err)
	}
	if got != "" {
		t.Errorf("resolveToken() = %q, want empty", got)
	}
}

func TestResolveToken_ExchangeFailsOnHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Invalid User API Key"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	httpClient := &http.Client{}
	_, err := resolveToken(httpClient, server.URL, "key_bad")
	if err == nil {
		t.Fatal("resolveToken() expected error for 401 response, got nil")
	}
}

func TestResolveToken_ExchangeFailsOnEmptyAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(exchangeResponse{AccessToken: "", RefreshToken: ""})
	}))
	defer server.Close()

	httpClient := &http.Client{}
	_, err := resolveToken(httpClient, server.URL, "key_empty_response")
	if err == nil {
		t.Fatal("resolveToken() expected error for empty accessToken, got nil")
	}
}

func TestResolveToken_ExchangeFailsOnWhitespaceAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(exchangeResponse{AccessToken: "   ", RefreshToken: ""})
	}))
	defer server.Close()

	httpClient := &http.Client{}
	_, err := resolveToken(httpClient, server.URL, "key_whitespace_response")
	if err == nil {
		t.Fatal("resolveToken() expected error for whitespace-only accessToken, got nil")
	}
}

func TestIsAPIKey(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{"key_abc123", true},
		{"key_", true},
		{"  key_abc123", true}, // trimmed
		{"session_token_here", false},
		{"eyJhbGciOi...", false},
		{"", false},
		{"KEY_abc", false}, // case-sensitive
		{"bearer key_abc", false},
	}
	for _, tt := range tests {
		if got := isAPIKey(tt.token); got != tt.want {
			t.Errorf("isAPIKey(%q) = %v, want %v", tt.token, got, tt.want)
		}
	}
}

func TestFormatAuthHeader(t *testing.T) {
	tests := []struct {
		token string
		want  string
	}{
		{"abc123", "Bearer abc123"},
		{"Bearer abc123", "Bearer abc123"},
		{"bearer abc123", "bearer abc123"},
		{"", ""},
		{"  abc123  ", "Bearer abc123"},
	}
	for _, tt := range tests {
		if got := formatAuthHeader(tt.token); got != tt.want {
			t.Errorf("formatAuthHeader(%q) = %q, want %q", tt.token, got, tt.want)
		}
	}
}
