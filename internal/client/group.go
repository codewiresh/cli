package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"strings"
	"time"

	"github.com/codewiresh/codewire/internal/store"
)

type RelayGroup struct {
	NetworkID string              `json:"network_id"`
	Name      string              `json:"name"`
	CreatedAt time.Time           `json:"created_at"`
	CreatedBy string              `json:"created_by,omitempty"`
	Members   []store.GroupMember `json:"members,omitempty"`
	Policy    *store.GroupPolicy  `json:"policy,omitempty"`
}

type GroupPolicyUpdateOptions struct {
	MessagesPolicy string
	DebugPolicy    string
}

func CreateGroup(dataDir, name string, auth RelayAuthOptions) (*RelayGroup, error) {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(map[string]any{
		"network_id": networkID,
		"name":       strings.TrimSpace(name),
	})
	if err != nil {
		return nil, fmt.Errorf("encoding group create request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(relayURL, "/")+"/api/v1/groups", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	var group RelayGroup
	if err := doRelayJSON(req, &group, "create group"); err != nil {
		return nil, err
	}
	return &group, nil
}

func ListGroups(dataDir string, auth RelayAuthOptions) ([]RelayGroup, error) {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return nil, err
	}

	query := urlpkg.Values{}
	query.Set("network_id", networkID)
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(relayURL, "/")+"/api/v1/groups?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)

	var groups []RelayGroup
	if err := doRelayJSON(req, &groups, "list groups"); err != nil {
		return nil, err
	}
	return groups, nil
}

func GetGroup(dataDir, name string, auth RelayAuthOptions) (*RelayGroup, error) {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(relayURL, "/") + "/api/v1/groups/" + urlpkg.PathEscape(strings.TrimSpace(name)) + "?network_id=" + urlpkg.QueryEscape(networkID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)

	var group RelayGroup
	if err := doRelayJSON(req, &group, "get group"); err != nil {
		return nil, err
	}
	return &group, nil
}

func DeleteGroup(dataDir, name string, auth RelayAuthOptions) error {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return err
	}

	url := strings.TrimRight(relayURL, "/") + "/api/v1/groups/" + urlpkg.PathEscape(strings.TrimSpace(name)) + "?network_id=" + urlpkg.QueryEscape(networkID)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	return doRelayJSON(req, nil, "delete group")
}

func AddGroupMember(dataDir, groupName, nodeName, sessionName string, auth RelayAuthOptions) (*store.GroupMember, error) {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(map[string]any{
		"network_id":   networkID,
		"node_name":    strings.TrimSpace(nodeName),
		"session_name": strings.TrimSpace(sessionName),
	})
	if err != nil {
		return nil, fmt.Errorf("encoding group member add request: %w", err)
	}

	url := strings.TrimRight(relayURL, "/") + "/api/v1/groups/" + urlpkg.PathEscape(strings.TrimSpace(groupName)) + "/members"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	var member store.GroupMember
	if err := doRelayJSON(req, &member, "add group member"); err != nil {
		return nil, err
	}
	return &member, nil
}

func RemoveGroupMember(dataDir, groupName, nodeName, sessionName string, auth RelayAuthOptions) error {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return err
	}

	query := urlpkg.Values{}
	query.Set("network_id", networkID)
	query.Set("node_name", strings.TrimSpace(nodeName))
	query.Set("session_name", strings.TrimSpace(sessionName))
	url := strings.TrimRight(relayURL, "/") + "/api/v1/groups/" + urlpkg.PathEscape(strings.TrimSpace(groupName)) + "/members?" + query.Encode()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	return doRelayJSON(req, nil, "remove group member")
}

func SetGroupPolicy(dataDir, groupName string, auth RelayAuthOptions, opts GroupPolicyUpdateOptions) (*store.GroupPolicy, error) {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(map[string]any{
		"network_id":      networkID,
		"messages_policy": strings.TrimSpace(opts.MessagesPolicy),
		"debug_policy":    strings.TrimSpace(opts.DebugPolicy),
	})
	if err != nil {
		return nil, fmt.Errorf("encoding group policy request: %w", err)
	}

	url := strings.TrimRight(relayURL, "/") + "/api/v1/groups/" + urlpkg.PathEscape(strings.TrimSpace(groupName)) + "/policy"
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	var policy store.GroupPolicy
	if err := doRelayJSON(req, &policy, "set group policy"); err != nil {
		return nil, err
	}
	return &policy, nil
}

func doRelayJSON(req *http.Request, out any, action string) error {
	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("contacting relay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("failed to %s: %s", action, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("parsing %s response: %w", action, err)
	}
	return nil
}
