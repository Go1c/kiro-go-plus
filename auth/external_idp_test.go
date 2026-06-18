package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kiro-go-plus/config"
)

// TestRefreshTokenExternalIdpUsesSocialEndpoint verifies that enterprise
// external-IdP accounts (provider "ExternalIdp") refresh through Kiro's desktop
// auth endpoint — the same broker as social logins — rather than calling the
// IdP's own tokenEndpoint, whose tokens CodeWhisperer rejects with HTTP 403.
func TestRefreshTokenExternalIdpUsesSocialEndpoint(t *testing.T) {
	var gotPath string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"accessToken":"kiro-at","refreshToken":"kiro-rt","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/X"}`)
	}))
	defer server.Close()

	// RefreshToken consults config.GetProxyURL(), so config must be initialized.
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}

	// Install a clean global client (Proxy=nil) so issuing this request does not
	// cache http.ProxyFromEnvironment and poison sibling transport-proxy tests.
	prevClient := SetGlobalAuthClientForTest(&http.Client{Transport: &http.Transport{}})
	defer SetGlobalAuthClientForTest(prevClient)

	restore := SetSocialTokenURLForTest(func() string { return server.URL + "/refreshToken" })
	defer SetSocialTokenURLForTest(restore)

	acc := &config.Account{
		AuthMethod:    "external_idp",
		Provider:      "ExternalIdp",
		RefreshToken:  "entra-rt",
		TokenEndpoint: "https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		Scopes:        "api://abc/codewhisperer:conversations offline_access",
	}
	accessToken, refreshToken, _, profileArn, err := RefreshToken(acc)
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if accessToken != "kiro-at" || refreshToken != "kiro-rt" {
		t.Fatalf("unexpected tokens: access=%q refresh=%q", accessToken, refreshToken)
	}
	if profileArn != "arn:aws:codewhisperer:us-east-1:1:profile/X" {
		t.Fatalf("expected brokered profileArn, got %q", profileArn)
	}
	if gotPath != "/refreshToken" {
		t.Fatalf("expected social /refreshToken endpoint, got %q", gotPath)
	}
	if !strings.Contains(gotBody, `"refreshToken":"entra-rt"`) {
		t.Fatalf("expected refreshToken in body, got %q", gotBody)
	}
}
