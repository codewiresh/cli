package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"strings"

	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/store"
)

type CreateAccessGrantOptions struct {
	TargetNode  string
	SessionID   *uint32
	SessionName string
	Audience    string
	Verbs       []string
	TTL         string
}

type ListAccessGrantOptions struct {
	TargetNode string
	Audience   string
	ActiveOnly bool
	Mine       bool
}

func CreateAccessGrant(dataDir string, auth RelayAuthOptions, opts CreateAccessGrantOptions) (*networkauth.ObserverDelegationResponse, error) {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"network_id":   networkID,
		"target_node":  strings.TrimSpace(opts.TargetNode),
		"session_name": strings.TrimSpace(opts.SessionName),
		"audience":     strings.TrimSpace(opts.Audience),
		"verbs":        append([]string(nil), opts.Verbs...),
		"ttl":          strings.TrimSpace(opts.TTL),
	}
	if opts.SessionID != nil {
		body["session_id"] = *opts.SessionID
		delete(body, "session_name")
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding access grant request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(relayURL, "/")+"/api/v1/access-grants", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting relay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("failed to create access grant: %s", strings.TrimSpace(string(body)))
	}

	var issued networkauth.ObserverDelegationResponse
	if err := json.NewDecoder(resp.Body).Decode(&issued); err != nil {
		return nil, fmt.Errorf("parsing access grant response: %w", err)
	}
	return &issued, nil
}

func ListAccessGrants(dataDir string, auth RelayAuthOptions, opts ListAccessGrantOptions) ([]store.AccessGrant, error) {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return nil, err
	}

	query := urlpkg.Values{}
	query.Set("network_id", networkID)
	if strings.TrimSpace(opts.TargetNode) != "" {
		query.Set("target_node", strings.TrimSpace(opts.TargetNode))
	}
	if strings.TrimSpace(opts.Audience) != "" {
		query.Set("audience", strings.TrimSpace(opts.Audience))
	}
	if opts.ActiveOnly {
		query.Set("active", "true")
	}
	if opts.Mine {
		query.Set("mine", "true")
	}

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(relayURL, "/")+"/api/v1/access-grants?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting relay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("failed to list access grants: %s", strings.TrimSpace(string(body)))
	}

	var grants []store.AccessGrant
	if err := json.NewDecoder(resp.Body).Decode(&grants); err != nil {
		return nil, fmt.Errorf("parsing access grant list: %w", err)
	}
	return grants, nil
}

func GetAccessGrant(dataDir, grantID string, auth RelayAuthOptions) (*store.AccessGrant, error) {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(relayURL, "/") + "/api/v1/access-grants/" + urlpkg.PathEscape(strings.TrimSpace(grantID))
	if networkID != "" {
		url += "?network_id=" + urlpkg.QueryEscape(networkID)
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting relay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("failed to get access grant: %s", strings.TrimSpace(string(body)))
	}

	var grant store.AccessGrant
	if err := json.NewDecoder(resp.Body).Decode(&grant); err != nil {
		return nil, fmt.Errorf("parsing access grant response: %w", err)
	}
	return &grant, nil
}

func RevokeAccessGrant(dataDir, grantID string, auth RelayAuthOptions) error {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return err
	}

	url := strings.TrimRight(relayURL, "/") + "/api/v1/access-grants/" + urlpkg.PathEscape(strings.TrimSpace(grantID))
	if networkID != "" {
		url += "?network_id=" + urlpkg.QueryEscape(networkID)
	}
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("contacting relay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("failed to revoke access grant: %s", strings.TrimSpace(string(body)))
	}
	return nil
}
