package proxy

import (
	"encoding/json"
	"fmt"
	"kiro-go-plus/auth"
	"kiro-go-plus/config"
	accountpool "kiro-go-plus/pool"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// installCleanAuthClient replaces the global auth HTTP client with one whose
// Transport does not consult http.ProxyFromEnvironment — that function caches
// env vars on first call and would otherwise poison TestBuildKiroTransport*
// when tests run in the default order. Returns a cleanup that restores the
// previous client.
func installCleanAuthClient(t *testing.T) func() {
	t.Helper()
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}}
	prev := auth.SetGlobalAuthClientForTest(c)
	return func() { auth.SetGlobalAuthClientForTest(prev) }
}

// TestApiImportCredentialsRejectsWhenRefreshFails verifies the regression:
// previously, when auth.RefreshToken failed and the user supplied an accessToken,
// the handler stored that accessToken with ExpiresAt = now+300, producing an
// account that the pool would skip (Pick uses now > ExpiresAt-120 → ~3 min) and
// that the on-demand refresh path could never repair (Pick filters it out before
// ensureValidToken runs). The fix is to reject the import outright; the caller
// must provide a refreshToken that actually works.
func TestApiImportCredentialsRejectsWhenRefreshFails(t *testing.T) {
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	defer installCleanAuthClient(t)()

	// Stand up a fake OIDC endpoint that always 400s, simulating an unreachable
	// or invalid refresh.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer fake.Close()

	oldOIDC := authOidcURL()
	auth.SetOIDCTokenURLForTest(func(string) string { return fake.URL })
	defer auth.SetOIDCTokenURLForTest(oldOIDC)

	h := &Handler{pool: accountpool.GetPool()}

	body := `{"refreshToken":"rt-broken","accessToken":"at-still-valid-elsewhere","clientId":"c","clientSecret":"s","authMethod":"idc","region":"us-east-1"}`
	req := httptest.NewRequest("POST", "/auth/credentials", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.apiImportCredentials(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when refresh fails, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp["error"], "Token refresh failed") {
		t.Fatalf("expected refresh-failed error, got %q", resp["error"])
	}

	// Crucial: no account should have been created. The previous bug stored a
	// half-broken account with ExpiresAt ~now+300 that would die in 3 minutes.
	if accs := config.GetAccounts(); len(accs) != 0 {
		t.Fatalf("expected no accounts to be persisted on failed import, got %+v", accs)
	}
}

// TestApiImportCredentialsUsesUpstreamExpiresAt verifies the happy path: when
// refresh succeeds, the persisted ExpiresAt reflects the upstream expiresIn,
// not a hard-coded 300s.
func TestApiImportCredentialsUsesUpstreamExpiresAt(t *testing.T) {
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	defer installCleanAuthClient(t)()

	const upstreamExpiresIn = 3600
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"accessToken":"at-new","refreshToken":"rt-rotated","expiresIn":%d,"profileArn":"arn:aws:codewhisperer:profile/test"}`, upstreamExpiresIn)
	}))
	defer fake.Close()

	oldOIDC := authOidcURL()
	auth.SetOIDCTokenURLForTest(func(string) string { return fake.URL })
	defer auth.SetOIDCTokenURLForTest(oldOIDC)

	h := &Handler{pool: accountpool.GetPool()}

	before := time.Now().Unix()
	body := `{"refreshToken":"rt-good","clientId":"c","clientSecret":"s","authMethod":"idc","region":"us-east-1"}`
	req := httptest.NewRequest("POST", "/auth/credentials", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.apiImportCredentials(rec, req)
	after := time.Now().Unix()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on successful refresh, got %d body=%s", rec.Code, rec.Body.String())
	}

	accs := config.GetAccounts()
	if len(accs) != 1 {
		t.Fatalf("expected exactly one account persisted, got %d", len(accs))
	}
	got := accs[0]
	if got.AccessToken != "at-new" {
		t.Fatalf("expected upstream-issued accessToken, got %q", got.AccessToken)
	}
	if got.RefreshToken != "rt-rotated" {
		t.Fatalf("expected rotated refreshToken to be persisted, got %q", got.RefreshToken)
	}
	// Allow ±5s of drift but require the value to clearly come from upstream's
	// expiresIn rather than the old 300s fallback.
	expectMin := before + upstreamExpiresIn - 5
	expectMax := after + upstreamExpiresIn + 5
	if got.ExpiresAt < expectMin || got.ExpiresAt > expectMax {
		t.Fatalf("expected ExpiresAt ≈ now+%d ([%d..%d]), got %d", upstreamExpiresIn, expectMin, expectMax, got.ExpiresAt)
	}
	if got.ExpiresAt-time.Now().Unix() < 1500 {
		t.Fatalf("ExpiresAt too short — looks like the 300s fallback is still in play: %d (delta %d)", got.ExpiresAt, got.ExpiresAt-time.Now().Unix())
	}
}

func TestApiImportCredentialsPreservesProvidedProfileArn(t *testing.T) {
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	defer installCleanAuthClient(t)()

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"accessToken":"at-new","refreshToken":"rt-rotated","expiresIn":3600}`)
	}))
	defer fake.Close()

	oldOIDC := authOidcURL()
	auth.SetOIDCTokenURLForTest(func(string) string { return fake.URL })
	defer auth.SetOIDCTokenURLForTest(oldOIDC)

	h := &Handler{pool: accountpool.GetPool()}
	body := `{"refreshToken":"rt-good","clientId":"c","clientSecret":"s","authMethod":"idc","region":"eu-central-1","profileArn":"arn:aws:codewhisperer:eu-central-1:123:profile/ABC"}`
	req := httptest.NewRequest("POST", "/auth/credentials", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.apiImportCredentials(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on successful import, got %d body=%s", rec.Code, rec.Body.String())
	}
	accs := config.GetAccounts()
	if len(accs) != 1 {
		t.Fatalf("expected exactly one account persisted, got %d", len(accs))
	}
	if got := accs[0].ProfileArn; got != "arn:aws:codewhisperer:eu-central-1:123:profile/ABC" {
		t.Fatalf("profileArn = %q", got)
	}
}

func TestDecodeCredentialImportRequestAcceptsOriginalExportArray(t *testing.T) {
	body := `[{"type":"kiro","access_token":"at","refresh_token":"rt","client_id":"client","client_secret":"secret","region":"eu-central-1","auth_method":"idc","profile_arn":"arn:aws:codewhisperer:eu-central-1:123:profile/ABC"}]`

	got, err := decodeCredentialImportRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decodeCredentialImportRequest: %v", err)
	}
	if got.AccessToken != "at" || got.RefreshToken != "rt" || got.ClientID != "client" || got.ClientSecret != "secret" {
		t.Fatalf("credential fields were not mapped: %+v", got)
	}
	if got.Region != "eu-central-1" || got.AuthMethod != "idc" {
		t.Fatalf("region/authMethod were not mapped: %+v", got)
	}
	if got.ProfileArn != "arn:aws:codewhisperer:eu-central-1:123:profile/ABC" {
		t.Fatalf("profileArn = %q", got.ProfileArn)
	}
}

func TestDecodeCredentialImportRequestAcceptsExternalIdpExport(t *testing.T) {
	body := `[{"email":"user@corp.com","refreshToken":"rt","provider":"ExternalIdp","authMethod":"external_idp","tokenEndpoint":"https://login.microsoftonline.com/tenant/oauth2/v2.0/token","clientId":"abc","scopes":"api://abc/codewhisperer:conversations offline_access"}]`

	got, err := decodeCredentialImportRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decodeCredentialImportRequest: %v", err)
	}
	if got.Email != "user@corp.com" || got.RefreshToken != "rt" || got.ClientID != "abc" {
		t.Fatalf("basic fields not mapped: %+v", got)
	}
	if got.AuthMethod != "external_idp" || got.Provider != "ExternalIdp" {
		t.Fatalf("authMethod/provider not mapped: %+v", got)
	}
	if got.TokenEndpoint != "https://login.microsoftonline.com/tenant/oauth2/v2.0/token" {
		t.Fatalf("tokenEndpoint = %q", got.TokenEndpoint)
	}
	if got.Scopes != "api://abc/codewhisperer:conversations offline_access" {
		t.Fatalf("scopes = %q", got.Scopes)
	}
}

func TestDecodeCredentialImportRequestAcceptsScopesArray(t *testing.T) {
	body := `{"refreshToken":"rt","tokenEndpoint":"https://idp/token","clientId":"abc","scopes":["s1","s2","offline_access"]}`
	got, err := decodeCredentialImportRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decodeCredentialImportRequest: %v", err)
	}
	if got.Scopes != "s1 s2 offline_access" {
		t.Fatalf("scopes array not joined: %q", got.Scopes)
	}
}

// TestApiImportCredentialsExternalIdp exercises the full Kiro Account Manager
// external-IdP export: the handler must refresh through Kiro's social/desktop
// broker (NOT the IdP's tokenEndpoint), keep authMethod as external_idp, persist
// the brokered profileArn, and fall back to the export's email when getUserInfo
// yields nothing.
func TestApiImportCredentialsExternalIdp(t *testing.T) {
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	defer installCleanAuthClient(t)()

	var refreshHit bool
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.Header().Set("Content-Type", "application/json")
		// Kiro's broker returns a CodeWhisperer-usable token + profileArn.
		fmt.Fprint(w, `{"accessToken":"kiro-at","refreshToken":"kiro-rt","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:us-east-1:123:profile/X"}`)
	}))
	defer broker.Close()

	restore := auth.SetSocialTokenURLForTest(func() string { return broker.URL + "/refreshToken" })
	defer auth.SetSocialTokenURLForTest(restore)

	h := &Handler{pool: accountpool.GetPool()}
	body := `[{"email":"user@corp.com","refreshToken":"entra-rt","provider":"ExternalIdp","authMethod":"external_idp","tokenEndpoint":"https://login.microsoftonline.com/tenant/oauth2/v2.0/token","clientId":"abc","scopes":"api://abc/codewhisperer:conversations offline_access"}]`
	req := httptest.NewRequest("POST", "/auth/credentials", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.apiImportCredentials(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !refreshHit {
		t.Fatal("Kiro social/desktop broker was never called")
	}
	accs := config.GetAccounts()
	if len(accs) != 1 {
		t.Fatalf("expected one account, got %d", len(accs))
	}
	got := accs[0]
	if got.AuthMethod != "external_idp" {
		t.Fatalf("authMethod = %q, want external_idp", got.AuthMethod)
	}
	if got.Provider != "ExternalIdp" {
		t.Fatalf("provider = %q", got.Provider)
	}
	if got.AccessToken != "kiro-at" || got.RefreshToken != "kiro-rt" {
		t.Fatalf("tokens not persisted from broker refresh: %+v", got)
	}
	if got.ProfileArn != "arn:aws:codewhisperer:us-east-1:123:profile/X" {
		t.Fatalf("brokered profileArn not persisted: %q", got.ProfileArn)
	}
	// Origin metadata is preserved from the export for provenance.
	if got.TokenEndpoint == "" || got.Scopes == "" {
		t.Fatalf("origin metadata not persisted: endpoint=%q scopes=%q", got.TokenEndpoint, got.Scopes)
	}
	// getUserInfo will fail against the fake endpoint, so the email must come
	// from the export payload.
	if got.Email != "user@corp.com" {
		t.Fatalf("email = %q, want fallback to export email", got.Email)
	}
}

func TestDecodeCredentialImportRequestRejectsMultiAccountArray(t *testing.T) {
	_, err := decodeCredentialImportRequest(strings.NewReader(`[{"refresh_token":"one"},{"refresh_token":"two"}]`))
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected multi-account array error, got %v", err)
	}
}

// authOidcURL captures the current oidc URL builder so the test can restore it.
func authOidcURL() func(string) string { return auth.GetOIDCTokenURLForTest() }
