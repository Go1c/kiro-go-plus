package proxy

import (
	"fmt"
	"kiro-go-plus/config"
	"net/url"
	"regexp"
	"strings"
)

var kiroRegionPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

func regionForAccount(account *config.Account) string {
	if account != nil {
		if region := strings.TrimSpace(account.Region); kiroRegionPattern.MatchString(region) {
			return region
		}

		parts := strings.Split(strings.TrimSpace(account.ProfileArn), ":")
		if len(parts) > 3 && kiroRegionPattern.MatchString(parts[3]) {
			return parts[3]
		}
	}
	return "us-east-1"
}

func kiroManagementAPIBase(account *config.Account) string {
	return fmt.Sprintf("https://management.%s.kiro.dev", regionForAccount(account))
}

func regionalizeKiroEndpoint(rawURL string, account *config.Account) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	region := regionForAccount(account)
	switch parsed.Host {
	case "runtime.us-east-1.kiro.dev":
		parsed.Host = fmt.Sprintf("runtime.%s.kiro.dev", region)
	case "q.us-east-1.amazonaws.com":
		parsed.Host = fmt.Sprintf("q.%s.amazonaws.com", region)
	case "codewhisperer.us-east-1.amazonaws.com":
		parsed.Host = fmt.Sprintf("codewhisperer.%s.amazonaws.com", region)
	default:
		return rawURL
	}
	return parsed.String()
}
