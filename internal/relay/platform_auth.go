package relay

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	urlpkg "net/url"
	"strings"
	"time"

	"github.com/codewiresh/codewire/internal/oauth"
)

var upstreamPlatformAuthHTTPClient = &http.Client{Timeout: 10 * time.Second}

type platformSessionResponse struct {
	User *struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
}

func platformSessionAuthValidator(issuer string) oauth.ExternalTokenValidator {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return nil
	}
	baseURL, err := urlpkg.Parse(issuer)
	if err != nil || strings.TrimSpace(baseURL.Scheme) == "" || strings.TrimSpace(baseURL.Host) == "" {
		return nil
	}

	sessionURL := strings.TrimRight(baseURL.String(), "/") + "/api/auth/get-session"
	return func(ctx context.Context, token string) *oauth.AuthIdentity {
		token = strings.TrimSpace(token)
		if token == "" {
			return nil
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, sessionURL, nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := upstreamPlatformAuthHTTPClient.Do(req)
		if err != nil {
			slog.Warn("relay platform auth request failed", "url", sessionURL, "error", err)
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			slog.Warn("relay platform auth rejected token", "url", sessionURL, "status", resp.StatusCode)
			return nil
		}

		var sessionResp platformSessionResponse
		if err := json.NewDecoder(resp.Body).Decode(&sessionResp); err != nil {
			slog.Warn("relay platform auth decode failed", "url", sessionURL, "error", err)
			return nil
		}
		if sessionResp.User == nil {
			slog.Warn("relay platform auth returned no user", "url", sessionURL)
			return nil
		}

		username := strings.TrimSpace(sessionResp.User.Email)
		if username == "" {
			username = strings.TrimSpace(sessionResp.User.Name)
		}
		sub := strings.TrimSpace(sessionResp.User.ID)
		if sub != "" {
			sub = "user:" + sub
		}

		if sub == "" && username == "" {
			return nil
		}
		return &oauth.AuthIdentity{
			Sub:      sub,
			Username: username,
		}
	}
}
