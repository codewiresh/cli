package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/codewiresh/codewire/internal/networkauth"
)

type AcceptedAccessGrant struct {
	Token               string    `json:"token"`
	GrantID             string    `json:"grant_id"`
	NetworkID           string    `json:"network_id"`
	TargetNode          string    `json:"target_node"`
	SessionID           *uint32   `json:"session_id,omitempty"`
	SessionName         string    `json:"session_name,omitempty"`
	Verbs               []string  `json:"verbs"`
	AudienceSubjectKind string    `json:"audience_subject_kind"`
	AudienceSubjectID   string    `json:"audience_subject_id"`
	AcceptedAt          time.Time `json:"accepted_at"`
	ExpiresAt           time.Time `json:"expires_at"`
}

type acceptedGrantFile struct {
	Grants []AcceptedAccessGrant `json:"grants"`
}

type AcceptedGrantPruneResult struct {
	RemovedExpired int  `json:"removed_expired"`
	RemovedRevoked int  `json:"removed_revoked"`
	RemovedMissing int  `json:"removed_missing"`
	Remaining      int  `json:"remaining"`
	RelayChecked   bool `json:"relay_checked"`
}

type ListAcceptedAccessGrantOptions struct {
	NetworkID   string
	TargetNode  string
	SessionName string
	Verb        string
}

func AcceptAccessGrant(dataDir, token string) (*AcceptedAccessGrant, error) {
	claims, err := networkauth.ParseObserverDelegation(strings.TrimSpace(token))
	if err != nil {
		return nil, err
	}

	record := AcceptedAccessGrant{
		Token:               strings.TrimSpace(token),
		GrantID:             claims.JTI,
		NetworkID:           claims.NetworkID,
		TargetNode:          claims.TargetNode,
		SessionID:           claims.SessionID,
		SessionName:         claims.SessionName,
		Verbs:               append([]string(nil), claims.Verbs...),
		AudienceSubjectKind: claims.AudienceSubjectKind,
		AudienceSubjectID:   claims.AudienceSubjectID,
		AcceptedAt:          time.Now().UTC(),
		ExpiresAt:           claims.ExpiresAt,
	}

	file, err := loadAcceptedGrantFile(dataDir)
	if err != nil {
		return nil, err
	}
	file.Grants = pruneAcceptedGrants(file.Grants, time.Now().UTC())

	replaced := false
	for i := range file.Grants {
		if file.Grants[i].GrantID == record.GrantID {
			file.Grants[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		file.Grants = append(file.Grants, record)
	}
	if err := saveAcceptedGrantFile(dataDir, file); err != nil {
		return nil, err
	}
	return &record, nil
}

func ListAcceptedAccessGrants(dataDir string) ([]AcceptedAccessGrant, error) {
	file, err := loadAcceptedGrantFile(dataDir)
	if err != nil {
		return nil, err
	}
	pruned := pruneAcceptedGrants(file.Grants, time.Now().UTC())
	if len(pruned) != len(file.Grants) {
		file.Grants = pruned
		if err := saveAcceptedGrantFile(dataDir, file); err != nil {
			return nil, err
		}
	}
	return pruned, nil
}

func ListAcceptedAccessGrantsFiltered(dataDir string, opts ListAcceptedAccessGrantOptions) ([]AcceptedAccessGrant, error) {
	grants, err := ListAcceptedAccessGrants(dataDir)
	if err != nil {
		return nil, err
	}
	filtered := make([]AcceptedAccessGrant, 0, len(grants))
	for _, grant := range grants {
		if strings.TrimSpace(opts.NetworkID) != "" && networkauth.ResolveNetworkID(grant.NetworkID) != networkauth.ResolveNetworkID(opts.NetworkID) {
			continue
		}
		if strings.TrimSpace(opts.TargetNode) != "" && strings.TrimSpace(grant.TargetNode) != strings.TrimSpace(opts.TargetNode) {
			continue
		}
		if strings.TrimSpace(opts.SessionName) != "" && strings.TrimSpace(grant.SessionName) != strings.TrimSpace(opts.SessionName) {
			continue
		}
		if strings.TrimSpace(opts.Verb) != "" && !acceptedGrantAllowsVerb(grant.Verbs, opts.Verb) {
			continue
		}
		filtered = append(filtered, grant)
	}
	return filtered, nil
}

func GetAcceptedAccessGrant(dataDir, grantID string) (*AcceptedAccessGrant, error) {
	grants, err := ListAcceptedAccessGrants(dataDir)
	if err != nil {
		return nil, err
	}
	grantID = strings.TrimSpace(grantID)
	for _, grant := range grants {
		if strings.TrimSpace(grant.GrantID) == grantID {
			grantCopy := grant
			return &grantCopy, nil
		}
	}
	return nil, fmt.Errorf("accepted access grant %q not found", grantID)
}

func ResolveAcceptedAccessGrant(dataDir, networkID, targetNode string, sessionID *uint32, sessionName, verb string) (string, error) {
	grants, err := ListAcceptedAccessGrants(dataDir)
	if err != nil {
		return "", err
	}

	matching := make([]AcceptedAccessGrant, 0, len(grants))
	for i, grant := range grants {
		if networkauth.ResolveNetworkID(grant.NetworkID) != networkauth.ResolveNetworkID(networkID) {
			continue
		}
		if strings.TrimSpace(grant.TargetNode) != strings.TrimSpace(targetNode) {
			continue
		}
		if !acceptedGrantMatchesSession(grant, sessionID, sessionName) {
			continue
		}
		if !acceptedGrantAllowsVerb(grant.Verbs, verb) {
			continue
		}
		matching = append(matching, grants[i])
	}
	if len(matching) == 0 {
		return "", fmt.Errorf("no accepted grant found for %s:%s", targetNode, sessionLabel(sessionID, sessionName))
	}
	sort.Slice(matching, func(i, j int) bool {
		return matching[i].ExpiresAt.After(matching[j].ExpiresAt)
	})

	var auth RelayAuthOptions
	if _, _, _, err := loadRelayAuth(dataDir, RelayAuthOptions{NetworkID: networkID}); err == nil {
		auth.NetworkID = networkID
	}

	for _, grant := range matching {
		if auth.NetworkID == "" {
			return grant.Token, nil
		}
		current, err := GetAccessGrant(dataDir, grant.GrantID, auth)
		if err != nil {
			if acceptedGrantLookupSaysMissing(err) {
				_ = RemoveAcceptedAccessGrant(dataDir, grant.GrantID)
				continue
			}
			return grant.Token, nil
		}
		if current == nil || current.RevokedAt != nil || time.Now().UTC().After(current.ExpiresAt) {
			_ = RemoveAcceptedAccessGrant(dataDir, grant.GrantID)
			continue
		}
		return grant.Token, nil
	}
	return "", fmt.Errorf("no accepted grant found for %s:%s", targetNode, sessionLabel(sessionID, sessionName))
}

func acceptedGrantAllowsVerb(verbs []string, verb string) bool {
	verb = strings.TrimSpace(verb)
	for _, candidate := range verbs {
		if strings.TrimSpace(candidate) == verb {
			return true
		}
	}
	return false
}

func acceptedGrantMatchesSession(grant AcceptedAccessGrant, sessionID *uint32, sessionName string) bool {
	if sessionID != nil {
		return grant.SessionID != nil && *grant.SessionID == *sessionID
	}
	if strings.TrimSpace(sessionName) == "" {
		return false
	}
	return strings.TrimSpace(grant.SessionName) == strings.TrimSpace(sessionName)
}

func sessionLabel(sessionID *uint32, sessionName string) string {
	if sessionID != nil {
		return fmt.Sprintf("%d", *sessionID)
	}
	return sessionName
}

func acceptedGrantPath(dataDir string) string {
	return filepath.Join(dataDir, "accepted_access_grants.json")
}

func RemoveAcceptedAccessGrant(dataDir, grantID string) error {
	file, err := loadAcceptedGrantFile(dataDir)
	if err != nil {
		return err
	}
	removed := false
	next := file.Grants[:0]
	for _, grant := range file.Grants {
		if strings.TrimSpace(grant.GrantID) == strings.TrimSpace(grantID) {
			removed = true
			continue
		}
		next = append(next, grant)
	}
	if !removed {
		return fmt.Errorf("accepted access grant %q not found", strings.TrimSpace(grantID))
	}
	file.Grants = next
	return saveAcceptedGrantFile(dataDir, file)
}

func PruneAcceptedAccessGrants(dataDir string, auth RelayAuthOptions) (*AcceptedGrantPruneResult, error) {
	file, err := loadAcceptedGrantFile(dataDir)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result := &AcceptedGrantPruneResult{}
	remaining := make([]AcceptedAccessGrant, 0, len(file.Grants))
	for _, grant := range file.Grants {
		if now.After(grant.ExpiresAt) {
			result.RemovedExpired++
			continue
		}
		remaining = append(remaining, grant)
	}

	if _, _, _, err := loadRelayAuth(dataDir, auth); err == nil {
		result.RelayChecked = true
		validated := remaining[:0]
		for _, grant := range remaining {
			current, getErr := GetAccessGrant(dataDir, grant.GrantID, RelayAuthOptions{
				RelayURL:  auth.RelayURL,
				AuthToken: auth.AuthToken,
				NetworkID: grant.NetworkID,
			})
			if getErr != nil {
				if acceptedGrantLookupSaysMissing(getErr) {
					result.RemovedMissing++
					continue
				}
				validated = append(validated, grant)
				continue
			}
			if current == nil {
				result.RemovedMissing++
				continue
			}
			if current.RevokedAt != nil || now.After(current.ExpiresAt) {
				result.RemovedRevoked++
				continue
			}
			validated = append(validated, grant)
		}
		remaining = validated
	}

	file.Grants = remaining
	result.Remaining = len(remaining)
	if err := saveAcceptedGrantFile(dataDir, file); err != nil {
		return nil, err
	}
	return result, nil
}

func loadAcceptedGrantFile(dataDir string) (*acceptedGrantFile, error) {
	path := acceptedGrantPath(dataDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &acceptedGrantFile{}, nil
		}
		return nil, fmt.Errorf("reading accepted access grants: %w", err)
	}

	var file acceptedGrantFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parsing accepted access grants: %w", err)
	}
	return &file, nil
}

func saveAcceptedGrantFile(dataDir string, file *acceptedGrantFile) error {
	if file == nil {
		file = &acceptedGrantFile{}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding accepted access grants: %w", err)
	}
	if err := os.WriteFile(acceptedGrantPath(dataDir), data, 0o600); err != nil {
		return fmt.Errorf("writing accepted access grants: %w", err)
	}
	return nil
}

func pruneAcceptedGrants(grants []AcceptedAccessGrant, now time.Time) []AcceptedAccessGrant {
	pruned := make([]AcceptedAccessGrant, 0, len(grants))
	for _, grant := range grants {
		if now.After(grant.ExpiresAt) {
			continue
		}
		pruned = append(pruned, grant)
	}
	return pruned
}

func acceptedGrantLookupSaysMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "access grant not found") || strings.Contains(msg, "membership required")
}
