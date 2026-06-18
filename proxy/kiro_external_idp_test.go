package proxy

import (
	"net/http"
	"testing"

	"kiro-go-plus/config"
)

// TestApplyKiroBaseHeadersExternalIdpTokenType verifies the EXTERNAL_IDP token
// type header is set only for external_idp accounts. Without it, CodeWhisperer
// silently returns an empty profile list and rejects data-plane calls for
// enterprise SSO (Azure AD) tokens.
func TestApplyKiroBaseHeadersExternalIdpTokenType(t *testing.T) {
	cases := []struct {
		authMethod string
		wantHeader bool
	}{
		{"external_idp", true},
		{"idc", false},
		{"social", false},
		{"", false},
	}
	for _, tc := range cases {
		req, _ := http.NewRequest("POST", "https://example.com/", nil)
		acc := &config.Account{AccessToken: "tok", AuthMethod: tc.authMethod}
		applyKiroBaseHeaders(req, acc, kiroHeaderValues{UserAgent: "ua", AmzUserAgent: "amz"})

		got := req.Header.Get("TokenType")
		if tc.wantHeader && got != "EXTERNAL_IDP" {
			t.Errorf("authMethod=%q: TokenType = %q, want EXTERNAL_IDP", tc.authMethod, got)
		}
		if !tc.wantHeader && got != "" {
			t.Errorf("authMethod=%q: TokenType = %q, want empty", tc.authMethod, got)
		}
	}
}
