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
	"strconv"
	"strings"
	"time"
)

type TaskSnapshot struct {
	EventID     string `json:"event_id"`
	StreamSeq   uint64 `json:"stream_seq"`
	NetworkID   string `json:"network_id"`
	NodeName    string `json:"node_name"`
	SessionID   uint32 `json:"session_id"`
	SessionName string `json:"session_name,omitempty"`
	Summary     string `json:"summary"`
	State       string `json:"state"`
	Timestamp   string `json:"timestamp"`
}

type TaskEvent struct {
	Seq         uint64 `json:"seq"`
	Type        string `json:"type"`
	EventID     string `json:"event_id,omitempty"`
	NetworkID   string `json:"network_id"`
	NodeName    string `json:"node_name,omitempty"`
	SessionID   uint32 `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	Summary     string `json:"summary,omitempty"`
	State       string `json:"state,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
}

type WatchTasksOptions struct {
	NetworkID string
	NodeName  string
	SessionID *uint32
	State     string
}

type taskStateFile struct {
	Networks map[string]taskNetworkState `json:"networks"`
}

type taskNetworkState struct {
	LastEventID string `json:"last_event_id,omitempty"`
}

func ListTasks(dataDir string, auth RelayAuthOptions, opts WatchTasksOptions) ([]TaskSnapshot, error) {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return nil, err
	}
	networkID = strings.TrimSpace(firstNonEmpty(opts.NetworkID, networkID))
	if networkID == "" {
		return nil, fmt.Errorf("network not configured (select a network or pass --network)")
	}

	req, err := http.NewRequest(http.MethodGet, taskURL(relayURL, networkID, opts), nil)
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
		return nil, fmt.Errorf("failed to list tasks: %s", strings.TrimSpace(string(body)))
	}

	var snapshots []TaskSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshots); err != nil {
		return nil, fmt.Errorf("parsing tasks: %w", err)
	}
	return snapshots, nil
}

func WatchTasks(ctx context.Context, dataDir string, auth RelayAuthOptions, opts WatchTasksOptions, out chan<- TaskEvent) error {
	relayURL, authToken, networkID, err := loadRelayAuth(dataDir, auth)
	if err != nil {
		return err
	}
	networkID = strings.TrimSpace(firstNonEmpty(opts.NetworkID, networkID))
	if networkID == "" {
		return fmt.Errorf("network not configured (select a network or pass --network)")
	}
	opts.NetworkID = networkID

	backoff := time.Second
	for {
		lastEventID, err := taskLastEventID(dataDir, networkID)
		if err != nil {
			return err
		}
		err = streamTaskEvents(ctx, relayURL, authToken, opts, lastEventID, func(eventID string, ev TaskEvent) error {
			if strings.TrimSpace(eventID) != "" {
				if err := setTaskLastEventID(dataDir, networkID, eventID); err != nil {
					return err
				}
			}
			select {
			case out <- ev:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err == nil || ctx.Err() != nil {
			if ctx.Err() != nil {
				return nil
			}
			backoff = time.Second
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

func taskURL(relayURL, networkID string, opts WatchTasksOptions) string {
	query := urlpkg.Values{}
	query.Set("network_id", strings.TrimSpace(networkID))
	if node := strings.TrimSpace(opts.NodeName); node != "" {
		query.Set("node", node)
	}
	if opts.SessionID != nil {
		query.Set("session_id", strconv.FormatUint(uint64(*opts.SessionID), 10))
	}
	if state := strings.TrimSpace(opts.State); state != "" {
		query.Set("state", state)
	}
	return strings.TrimRight(relayURL, "/") + "/api/v1/tasks?" + query.Encode()
}

func taskEventsURL(relayURL string, opts WatchTasksOptions) string {
	query := urlpkg.Values{}
	query.Set("network_id", strings.TrimSpace(opts.NetworkID))
	if node := strings.TrimSpace(opts.NodeName); node != "" {
		query.Set("node", node)
	}
	if opts.SessionID != nil {
		query.Set("session_id", strconv.FormatUint(uint64(*opts.SessionID), 10))
	}
	if state := strings.TrimSpace(opts.State); state != "" {
		query.Set("state", state)
	}
	return strings.TrimRight(relayURL, "/") + "/api/v1/tasks/events?" + query.Encode()
}

func streamTaskEvents(ctx context.Context, relayURL, authToken string, opts WatchTasksOptions, lastEventID string, handle func(eventID string, ev TaskEvent) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, taskEventsURL(relayURL, opts), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+authToken)
	if strings.TrimSpace(lastEventID) != "" {
		req.Header.Set("Last-Event-ID", strings.TrimSpace(lastEventID))
	}

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("contacting relay: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("failed to watch tasks: %s", strings.TrimSpace(string(body)))
	}

	scanner := bufio.NewScanner(resp.Body)
	var eventID string
	var dataLines []string
	dispatch := func() error {
		if len(dataLines) == 0 {
			eventID = ""
			return nil
		}
		var ev TaskEvent
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &ev); err != nil {
			eventID = ""
			dataLines = nil
			return nil
		}
		if err := handle(strings.TrimSpace(eventID), ev); err != nil {
			return err
		}
		eventID = ""
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

func taskStatePath(dataDir string) string {
	return filepath.Join(dataDir, "tasks_state.json")
}

func taskLastEventID(dataDir, networkID string) (string, error) {
	state, err := loadTaskState(dataDir)
	if err != nil {
		return "", err
	}
	return state.Networks[strings.TrimSpace(networkID)].LastEventID, nil
}

func setTaskLastEventID(dataDir, networkID, eventID string) error {
	state, err := loadTaskState(dataDir)
	if err != nil {
		return err
	}
	if state.Networks == nil {
		state.Networks = map[string]taskNetworkState{}
	}
	key := strings.TrimSpace(networkID)
	current := state.Networks[key]
	current.LastEventID = strings.TrimSpace(eventID)
	state.Networks[key] = current
	return saveTaskState(dataDir, state)
}

func loadTaskState(dataDir string) (*taskStateFile, error) {
	path := taskStatePath(dataDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &taskStateFile{Networks: map[string]taskNetworkState{}}, nil
		}
		return nil, fmt.Errorf("reading task watch state: %w", err)
	}

	var state taskStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing task watch state: %w", err)
	}
	if state.Networks == nil {
		state.Networks = map[string]taskNetworkState{}
	}
	return &state, nil
}

func saveTaskState(dataDir string, state *taskStateFile) error {
	if state == nil {
		state = &taskStateFile{}
	}
	if state.Networks == nil {
		state.Networks = map[string]taskNetworkState{}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding task watch state: %w", err)
	}
	if err := os.WriteFile(taskStatePath(dataDir), data, 0o600); err != nil {
		return fmt.Errorf("writing task watch state: %w", err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
