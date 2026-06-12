package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestRefreshOIDCTokenFallsBackToUSEast1(t *testing.T) {
	var requestedRegions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		region := r.URL.Path[1:]
		requestedRegions = append(requestedRegions, region)
		w.Header().Set("Content-Type", "application/json")
		if region == "eu-central-1" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_request","error_description":"Invalid token provided"}`)
			return
		}
		fmt.Fprint(w, `{"accessToken":"access","refreshToken":"refresh-rotated","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:eu-central-1:123:profile/ABC"}`)
	}))
	defer server.Close()

	oldOIDC := oidcTokenURL
	oidcTokenURL = func(region string) string { return server.URL + "/" + region }
	defer func() { oidcTokenURL = oldOIDC }()

	accessToken, refreshToken, _, profileArn, err := refreshOIDCToken(
		"refresh", "client", "secret", "eu-central-1", server.Client(),
	)
	if err != nil {
		t.Fatalf("refreshOIDCToken: %v", err)
	}
	if accessToken != "access" || refreshToken != "refresh-rotated" {
		t.Fatalf("unexpected tokens returned")
	}
	if profileArn != "arn:aws:codewhisperer:eu-central-1:123:profile/ABC" {
		t.Fatalf("unexpected profile ARN: %q", profileArn)
	}
	if want := []string{"eu-central-1", "us-east-1"}; !reflect.DeepEqual(requestedRegions, want) {
		t.Fatalf("requested regions = %v, want %v", requestedRegions, want)
	}
}
