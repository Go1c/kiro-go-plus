package auth

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestRefreshExternalIdpToken verifies that external-IdP (enterprise SSO) accounts
// refresh against the IdP token endpoint with an OAuth2 refresh_token grant for a
// public client (no secret), and that the snake_case response is mapped correctly.
func TestRefreshExternalIdpToken(t *testing.T) {
	var gotForm url.Values
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token_type":"Bearer","access_token":"entra-access","refresh_token":"entra-rotated","expires_in":3600,"id_token":"id"}`)
	}))
	defer server.Close()

	accessToken, refreshToken, expiresAt, profileArn, err := refreshExternalIdpToken(
		"old-refresh", "client-123", server.URL, "scope-a scope-b offline_access", server.Client(),
	)
	if err != nil {
		t.Fatalf("refreshExternalIdpToken: %v", err)
	}
	if accessToken != "entra-access" || refreshToken != "entra-rotated" {
		t.Fatalf("unexpected tokens: access=%q refresh=%q", accessToken, refreshToken)
	}
	if expiresAt == 0 {
		t.Fatalf("expiresAt should be set")
	}
	if profileArn != "" {
		t.Fatalf("external IdP refresh must not return a profileArn, got %q", profileArn)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("unexpected content type: %q", gotContentType)
	}
	if gotForm.Get("grant_type") != "refresh_token" ||
		gotForm.Get("client_id") != "client-123" ||
		gotForm.Get("refresh_token") != "old-refresh" ||
		gotForm.Get("scope") != "scope-a scope-b offline_access" {
		t.Fatalf("unexpected form: %v", gotForm)
	}
}

// TestRefreshExternalIdpTokenRetainsRefreshToken verifies that when the IdP omits a
// rotated refresh_token on refresh (some IdPs do), the existing one is kept.
func TestRefreshExternalIdpTokenRetainsRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","expires_in":3600}`)
	}))
	defer server.Close()

	_, refreshToken, _, _, err := refreshExternalIdpToken("keep-me", "client", server.URL, "", server.Client())
	if err != nil {
		t.Fatalf("refreshExternalIdpToken: %v", err)
	}
	if refreshToken != "keep-me" {
		t.Fatalf("expected existing refresh token retained, got %q", refreshToken)
	}
}

func TestRefreshExternalIdpTokenRequiresClientAndEndpoint(t *testing.T) {
	if _, _, _, _, err := refreshExternalIdpToken("rt", "", "https://idp/token", "scope", http.DefaultClient); err == nil {
		t.Fatal("expected error when clientId is empty")
	}
	if _, _, _, _, err := refreshExternalIdpToken("rt", "client", "", "scope", http.DefaultClient); err == nil {
		t.Fatal("expected error when tokenEndpoint is empty")
	}
}

func TestRefreshExternalIdpTokenPropagatesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"expired"}`)
	}))
	defer server.Close()

	_, _, _, _, err := refreshExternalIdpToken("rt", "client", server.URL, "scope", server.Client())
	if err == nil {
		t.Fatal("expected error on non-2xx response")
	}
}
