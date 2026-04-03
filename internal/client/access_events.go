package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type AccessCacheEvent struct {
	Seq       int64      `json:"seq"`
	Type      string     `json:"type"`
	NetworkID string     `json:"network_id"`
	GrantID   string     `json:"grant_id,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type acceptedGrantStateFile struct {
	Networks map[string]acceptedGrantNetworkState `json:"networks"`
}

type acceptedGrantNetworkState struct {
	LastEventID string `json:"last_event_id,omitempty"`
}

func WatchAcceptedAccessGrants(ctx context.Context, dataDir string, auth RelayAuthOptions, out io.Writer) error {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return err
	}
	if strings.TrimSpace(networkID) == "" {
		return fmt.Errorf("network not configured (select a network or pass --network)")
	}

	if out == nil {
		out = io.Discard
	}
	fmt.Fprintf(out, "Watching access invalidation events for %s\n", networkID)

	backoff := time.Second
	for {
		lastEventID, err := acceptedGrantLastEventID(dataDir, networkID)
		if err != nil {
			return err
		}
		err = streamAcceptedAccessEvents(ctx, relayURL, authToken, networkID, lastEventID, func(eventID, eventType string, ev AccessCacheEvent) error {
			if eventID != "" {
				if err := setAcceptedGrantLastEventID(dataDir, networkID, eventID); err != nil {
					return err
				}
			}
			switch eventType {
			case "access.grant.revoked":
				if strings.TrimSpace(ev.GrantID) == "" {
					return nil
				}
				if err := RemoveAcceptedAccessGrant(dataDir, ev.GrantID); err != nil && !isAcceptedGrantNotFound(err) {
					return err
				}
				fmt.Fprintf(out, "Removed accepted grant %s after relay revocation\n", ev.GrantID)
			case "stream.reset":
				if _, err := PruneAcceptedAccessGrants(dataDir, RelayAuthOptions{
					RelayURL:  relayURL,
					AuthToken: authToken,
					NetworkID: networkID,
				}); err != nil {
					return err
				}
				fmt.Fprintf(out, "Reconciled accepted grants after relay stream reset\n")
			}
			return nil
		})
		if err == nil || ctx.Err() != nil {
			if ctx.Err() != nil {
				return nil
			}
			backoff = time.Second
		} else {
			fmt.Fprintf(out, "Access event stream disconnected: %v\n", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func streamAcceptedAccessEvents(ctx context.Context, relayURL, authToken, networkID, lastEventID string, handle func(eventID, eventType string, ev AccessCacheEvent) error) error {
	query := urlpkg.Values{}
	query.Set("network_id", strings.TrimSpace(networkID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(relayURL, "/")+"/api/v1/access/cache/events?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+authToken)
	if strings.TrimSpace(lastEventID) != "" {
		req.Header.Set("Last-Event-ID", strings.TrimSpace(lastEventID))
	}

	client := relayHTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("contacting relay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("failed to watch access events: %s", strings.TrimSpace(string(body)))
	}

	scanner := bufio.NewScanner(resp.Body)
	var eventID string
	var eventType string
	var dataLines []string
	dispatch := func() error {
		if len(dataLines) == 0 {
			eventID = ""
			eventType = ""
			return nil
		}
		var ev AccessCacheEvent
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &ev); err != nil {
			eventID = ""
			eventType = ""
			dataLines = nil
			return nil
		}
		if eventType == "" {
			eventType = strings.TrimSpace(ev.Type)
		}
		if err := handle(strings.TrimSpace(eventID), strings.TrimSpace(eventType), ev); err != nil {
			return err
		}
		eventID = ""
		eventType = ""
		dataLines = nil
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "id:"):
			eventID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := dispatch(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func acceptedGrantStatePath(dataDir string) string {
	return filepath.Join(dataDir, "accepted_access_grants_state.json")
}

func acceptedGrantLastEventID(dataDir, networkID string) (string, error) {
	state, err := loadAcceptedGrantState(dataDir)
	if err != nil {
		return "", err
	}
	return state.Networks[strings.TrimSpace(networkID)].LastEventID, nil
}

func setAcceptedGrantLastEventID(dataDir, networkID, eventID string) error {
	state, err := loadAcceptedGrantState(dataDir)
	if err != nil {
		return err
	}
	if state.Networks == nil {
		state.Networks = map[string]acceptedGrantNetworkState{}
	}
	key := strings.TrimSpace(networkID)
	current := state.Networks[key]
	current.LastEventID = strings.TrimSpace(eventID)
	state.Networks[key] = current
	return saveAcceptedGrantState(dataDir, state)
}

func loadAcceptedGrantState(dataDir string) (*acceptedGrantStateFile, error) {
	path := acceptedGrantStatePath(dataDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &acceptedGrantStateFile{Networks: map[string]acceptedGrantNetworkState{}}, nil
		}
		return nil, fmt.Errorf("reading accepted access grant state: %w", err)
	}

	var state acceptedGrantStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing accepted access grant state: %w", err)
	}
	if state.Networks == nil {
		state.Networks = map[string]acceptedGrantNetworkState{}
	}
	return &state, nil
}

func saveAcceptedGrantState(dataDir string, state *acceptedGrantStateFile) error {
	if state == nil {
		state = &acceptedGrantStateFile{}
	}
	if state.Networks == nil {
		state.Networks = map[string]acceptedGrantNetworkState{}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding accepted access grant state: %w", err)
	}
	if err := os.WriteFile(acceptedGrantStatePath(dataDir), data, 0o600); err != nil {
		return fmt.Errorf("writing accepted access grant state: %w", err)
	}
	return nil
}

func isAcceptedGrantNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(err.Error())), "accepted access grant") &&
		strings.Contains(strings.ToLower(strings.TrimSpace(err.Error())), "not found")
}
