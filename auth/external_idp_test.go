package auth

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

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
		t.Fatalf("external IdP refresh should not return a profileArn, got %q", profileArn)
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

func TestRefreshExternalIdpTokenRequiresEndpoint(t *testing.T) {
	if _, _, _, _, err := refreshExternalIdpToken("rt", "client", "", "scope", http.DefaultClient); err == nil {
		t.Fatal("expected error when tokenEndpoint is empty")
	}
}

func TestRefreshExternalIdpTokenPropagatesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer server.Close()

	if _, _, _, _, err := refreshExternalIdpToken("rt", "client", server.URL, "scope", server.Client()); err == nil {
		t.Fatal("expected error on non-200 response")
	}
}
