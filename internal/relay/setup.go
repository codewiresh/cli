package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	qrcode "github.com/skip2/go-qrcode"
)

type NodeEnrollmentResult struct {
	ID              string `json:"id"`
	NetworkID       string `json:"network_id"`
	NodeName        string `json:"node_name,omitempty"`
	EnrollmentToken string `json:"enrollment_token"`
}

type NodeRedeemResult struct {
	NodeToken    string `json:"node_token"`
	NodeName     string `json:"node_name"`
	NetworkID    string `json:"network_id"`
	EnrollmentID string `json:"enrollment_id,omitempty"`
}

func CreateNodeEnrollment(ctx context.Context, relayURL, networkID, nodeName, authToken string, uses int, ttl string) (*NodeEnrollmentResult, error) {
	body, _ := json.Marshal(map[string]string{
		"network_id": networkID,
		"node_name":  nodeName,
	})
	if uses > 0 || ttl != "" {
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if uses > 0 {
			payload["uses"] = uses
		}
		if ttl != "" {
			payload["ttl"] = ttl
		}
		body, _ = json.Marshal(payload)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/api/v1/node-enrollments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting relay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("enrollment creation failed (%d): %s", resp.StatusCode, b)
	}

	var result NodeEnrollmentResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing enrollment response: %w", err)
	}
	return &result, nil
}

func RedeemNodeEnrollment(ctx context.Context, relayURL, enrollmentToken, nodeName string) (*NodeRedeemResult, error) {
	body, _ := json.Marshal(map[string]string{
		"enrollment_token": enrollmentToken,
		"node_name":        nodeName,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/api/v1/node-enrollments/redeem", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting relay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("enrollment redemption failed (%d): %s", resp.StatusCode, b)
	}

	var result NodeRedeemResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing redemption response: %w", err)
	}
	return &result, nil
}

func RegisterWithAuthToken(ctx context.Context, relayURL, networkID, nodeName, authToken string) (string, error) {
	enrollment, err := CreateNodeEnrollment(ctx, relayURL, networkID, nodeName, authToken, 1, "10m")
	if err != nil {
		return "", err
	}
	redeemed, err := RedeemNodeEnrollment(ctx, relayURL, enrollment.EnrollmentToken, nodeName)
	if err != nil {
		return "", err
	}
	return redeemed.NodeToken, nil
}

type JoinResult struct {
	NetworkID string `json:"network_id"`
}

func JoinNetworkWithInvite(ctx context.Context, relayURL, authToken, inviteToken string) (*JoinResult, error) {
	body, _ := json.Marshal(map[string]string{
		"invite_token": inviteToken,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/api/v1/networks/join", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting relay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("invite rejected (%d): %s", resp.StatusCode, b)
	}

	var result JoinResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing join response: %w", err)
	}
	return &result, nil
}

// RegisterWithInvite exchanges an invite token for a node token.
func RegisterWithInvite(ctx context.Context, relayURL, nodeName, inviteToken string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"node_name":    nodeName,
		"invite_token": inviteToken,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/api/v1/join", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("contacting relay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("invite rejected (%d): %s", resp.StatusCode, b)
	}

	var result struct {
		NodeToken string `json:"node_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parsing join response: %w", err)
	}
	return result.NodeToken, nil
}

// SSHURI builds an ssh:// URI for the given relay and node credentials.
func SSHURI(relayURL, networkID, nodeName, nodeToken string, port int) string {
	host := extractHost(relayURL)
	user := nodeName
	if networkID != "" {
		user = networkID + "/" + nodeName
	}
	return fmt.Sprintf("ssh://%s:%s@%s:%d", user, nodeToken, host, port)
}

// extractHost returns the hostname from a URL, falling back to the raw string.
func extractHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return rawURL
	}
	return u.Hostname()
}

// printSetupQR renders a QR code to stderr using Unicode half-blocks.
func printSetupQR(content string) {
	q, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n(QR generation failed: %v)\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "\n%s\n", q.ToSmallString(false))
}
