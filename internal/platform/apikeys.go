package platform

import "fmt"

// APIKey represents a Codewire API key.
type APIKey struct {
	ID        string   `json:"id"`
	UserID    string   `json:"user_id"`
	OrgID     string   `json:"org_id"`
	Name      string   `json:"name"`
	KeyPrefix string   `json:"key_prefix"`
	Scopes    []string `json:"scopes"`
	ExpiresAt *string  `json:"expires_at,omitempty"`
	CreatedAt string   `json:"created_at"`
}

// CreateAPIKeyRequest is the body for creating an API key.
type CreateAPIKeyRequest struct {
	Name          string   `json:"name"`
	Scopes        []string `json:"scopes,omitempty"`
	ExpiresInDays *int     `json:"expires_in_days,omitempty"`
}

// CreateAPIKeyResponse includes the full key (only shown once).
type CreateAPIKeyResponse struct {
	APIKey
	Key string `json:"key"`
}

// CreateAPIKey creates a new API key for the organization.
func (c *Client) CreateAPIKey(orgID string, req *CreateAPIKeyRequest) (*CreateAPIKeyResponse, error) {
	var resp CreateAPIKeyResponse
	err := c.do("POST", fmt.Sprintf("/api/v1/organizations/%s/api-keys", orgID), req, &resp)
	return &resp, err
}

// ListAPIKeys lists all API keys for the organization.
func (c *Client) ListAPIKeys(orgID string) ([]APIKey, error) {
	var keys []APIKey
	err := c.do("GET", fmt.Sprintf("/api/v1/organizations/%s/api-keys", orgID), nil, &keys)
	return keys, err
}

// DeleteAPIKey deletes an API key by ID.
func (c *Client) DeleteAPIKey(orgID, keyID string) error {
	return c.do("DELETE", fmt.Sprintf("/api/v1/organizations/%s/api-keys/%s", orgID, keyID), nil, nil)
}
