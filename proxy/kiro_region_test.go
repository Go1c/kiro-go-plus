package proxy

import (
	"kiro-go-plus/config"
	"testing"
)

func TestKiroManagementAPIBaseUsesAccountRegion(t *testing.T) {
	account := &config.Account{Region: "eu-central-1"}
	if got := kiroManagementAPIBase(account); got != "https://management.eu-central-1.kiro.dev" {
		t.Fatalf("kiroManagementAPIBase = %q", got)
	}
}

func TestRegionForAccountFallsBackToProfileArn(t *testing.T) {
	account := &config.Account{ProfileArn: "arn:aws:codewhisperer:eu-central-1:123:profile/ABC"}
	if got := regionForAccount(account); got != "eu-central-1" {
		t.Fatalf("regionForAccount = %q", got)
	}
}

func TestRegionalizeKiroEndpointPreservesCustomTestServer(t *testing.T) {
	account := &config.Account{Region: "eu-central-1"}
	const rawURL = "http://127.0.0.1:1234/generateAssistantResponse"
	if got := regionalizeKiroEndpoint(rawURL, account); got != rawURL {
		t.Fatalf("regionalizeKiroEndpoint = %q", got)
	}
}

func TestRegionalizeKiroEndpointUsesAccountRegion(t *testing.T) {
	account := &config.Account{Region: "eu-central-1"}
	got := regionalizeKiroEndpoint("https://runtime.us-east-1.kiro.dev/generateAssistantResponse", account)
	want := "https://runtime.eu-central-1.kiro.dev/generateAssistantResponse"
	if got != want {
		t.Fatalf("regionalizeKiroEndpoint = %q, want %q", got, want)
	}
}
