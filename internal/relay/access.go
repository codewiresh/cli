package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/oauth"
	"github.com/codewiresh/codewire/internal/store"
)

type accessGrantCreateRequest struct {
	NetworkID   string   `json:"network_id"`
	TargetNode  string   `json:"target_node"`
	SessionID   *uint32  `json:"session_id,omitempty"`
	SessionName string   `json:"session_name,omitempty"`
	Audience    string   `json:"audience"`
	Verbs       []string `json:"verbs"`
	TTL         string   `json:"ttl,omitempty"`
}

func accessGrantCreateHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity := oauth.GetAuth(r.Context())
		if identity == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req accessGrantCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		networkID, err := requiredNetworkID(req.NetworkID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		owner, err := requireOwner(r.Context(), st, networkID, identity)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !owner {
			writeOwnerRequired(w)
			return
		}

		if strings.TrimSpace(req.TargetNode) == "" {
			http.Error(w, "target_node required", http.StatusBadRequest)
			return
		}
		if req.SessionID == nil && strings.TrimSpace(req.SessionName) == "" {
			http.Error(w, "session_id or session_name required", http.StatusBadRequest)
			return
		}
		if req.SessionID != nil && strings.TrimSpace(req.SessionName) != "" {
			http.Error(w, "session_id and session_name are mutually exclusive", http.StatusBadRequest)
			return
		}

		verbs, err := normalizeObserverVerbs(req.Verbs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ttl := networkauth.DefaultObserverTTL
		if strings.TrimSpace(req.TTL) != "" {
			ttl, err = time.ParseDuration(strings.TrimSpace(req.TTL))
			if err != nil {
				http.Error(w, "invalid ttl", http.StatusBadRequest)
				return
			}
		}
		if ttl <= 0 {
			http.Error(w, "ttl must be positive", http.StatusBadRequest)
			return
		}

		audienceKind, audienceID, audienceDisplay, err := resolveAccessGrantAudience(r, st, strings.TrimSpace(req.Audience))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		state, err := loadOrCreateIssuerState(r.Context(), st, networkID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		now := time.Now().UTC()
		delegation, claims, err := networkauth.SignObserverDelegation(
			state,
			strings.TrimSpace(req.TargetNode),
			req.SessionID,
			strings.TrimSpace(req.SessionName),
			verbs,
			audienceKind,
			audienceID,
			now,
			ttl,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		issuedBy, err := membershipSubject(identity)
		if err != nil && !identity.IsAdmin {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		grant := store.AccessGrant{
			ID:                  claims.JTI,
			NetworkID:           claims.NetworkID,
			TargetNode:          claims.TargetNode,
			SessionID:           claims.SessionID,
			SessionName:         claims.SessionName,
			Verbs:               append([]string(nil), claims.Verbs...),
			AudienceSubjectKind: claims.AudienceSubjectKind,
			AudienceSubjectID:   claims.AudienceSubjectID,
			AudienceDisplay:     audienceDisplay,
			IssuedBy:            issuedBy,
			CreatedAt:           now,
			ExpiresAt:           claims.ExpiresAt,
		}
		if err := st.AccessGrantCreate(r.Context(), grant); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(networkauth.ObserverDelegationResponse{
			Delegation:          delegation,
			GrantID:             claims.JTI,
			NetworkID:           claims.NetworkID,
			TargetNode:          claims.TargetNode,
			SessionID:           claims.SessionID,
			SessionName:         claims.SessionName,
			Verbs:               append([]string(nil), claims.Verbs...),
			AudienceSubjectKind: claims.AudienceSubjectKind,
			AudienceSubjectID:   claims.AudienceSubjectID,
			AudienceDisplay:     audienceDisplay,
			ExpiresAt:           claims.ExpiresAt,
		})
	}
}

func accessGrantListHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity := oauth.GetAuth(r.Context())
		if identity == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		networkID, err := requiredNetworkID(r.URL.Query().Get("network_id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		filter := store.AccessGrantFilter{
			TargetNode: strings.TrimSpace(r.URL.Query().Get("target_node")),
		}
		if mineRaw := strings.TrimSpace(r.URL.Query().Get("mine")); mineRaw != "" {
			mine, parseErr := strconv.ParseBool(mineRaw)
			if parseErr != nil {
				http.Error(w, "invalid mine filter", http.StatusBadRequest)
				return
			}
			if mine {
				subject, err := membershipSubject(identity)
				if err != nil {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				if _, ok, err := requireMembership(r.Context(), st, networkID, identity); err != nil {
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				} else if !ok {
					writeMembershipRequired(w)
					return
				}
				filter.AudienceSubjectID = subject
			}
		}
		if filter.AudienceSubjectID == "" {
			owner, err := requireOwner(r.Context(), st, networkID, identity)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !owner {
				writeOwnerRequired(w)
				return
			}
		}
		if activeRaw := strings.TrimSpace(r.URL.Query().Get("active")); activeRaw != "" {
			active, parseErr := strconv.ParseBool(activeRaw)
			if parseErr != nil {
				http.Error(w, "invalid active filter", http.StatusBadRequest)
				return
			}
			filter.ActiveOnly = active
		}
		if audience := strings.TrimSpace(r.URL.Query().Get("audience")); audience != "" && filter.AudienceSubjectID == "" {
			_, audienceID, _, err := resolveAccessGrantAudience(r, st, audience)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			filter.AudienceSubjectID = audienceID
		}

		grants, err := st.AccessGrantList(r.Context(), networkID, filter)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(grants)
	}
}

func accessGrantGetHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity := oauth.GetAuth(r.Context())
		if identity == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		networkID, err := requiredNetworkID(r.URL.Query().Get("network_id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		grantID := strings.TrimSpace(r.PathValue("id"))
		if grantID == "" {
			http.Error(w, "grant id required", http.StatusBadRequest)
			return
		}

		grant, err := st.AccessGrantGet(r.Context(), networkID, grantID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if grant == nil {
			http.Error(w, "access grant not found", http.StatusNotFound)
			return
		}

		subject, subjectErr := membershipSubject(identity)
		if subjectErr == nil && subject == grant.AudienceSubjectID {
			if _, ok, err := requireMembership(r.Context(), st, networkID, identity); err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			} else if !ok {
				writeMembershipRequired(w)
				return
			}
		} else {
			owner, err := requireOwner(r.Context(), st, networkID, identity)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !owner {
				writeOwnerRequired(w)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(grant)
	}
}

func accessGrantRevokeHandler(st store.Store, events *AccessEventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity := oauth.GetAuth(r.Context())
		if identity == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		networkID, err := requiredNetworkID(r.URL.Query().Get("network_id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		owner, err := requireOwner(r.Context(), st, networkID, identity)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !owner {
			writeOwnerRequired(w)
			return
		}

		grantID := strings.TrimSpace(r.PathValue("id"))
		if grantID == "" {
			http.Error(w, "grant id required", http.StatusBadRequest)
			return
		}
		grant, err := st.AccessGrantGet(r.Context(), networkID, grantID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if grant == nil {
			http.Error(w, "access grant not found", http.StatusNotFound)
			return
		}
		revokedAt := time.Now().UTC()
		if err := st.AccessGrantRevoke(r.Context(), networkID, grantID, revokedAt); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		events.PublishGrantRevoked(networkID, grantID, revokedAt)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":   "revoked",
			"grant_id": grantID,
		})
	}
}

func normalizeObserverVerbs(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{"msg.read", "msg.listen"}, nil
	}

	seen := map[string]struct{}{}
	verbs := make([]string, 0, len(raw))
	for _, verb := range raw {
		normalized := strings.TrimSpace(strings.ToLower(verb))
		switch normalized {
		case "read", "msg.read":
			normalized = "msg.read"
		case "listen", "msg.listen":
			normalized = "msg.listen"
		default:
			return nil, fmt.Errorf("unsupported verb %q", verb)
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		verbs = append(verbs, normalized)
	}
	if len(verbs) == 0 {
		return nil, fmt.Errorf("at least one verb required")
	}
	return verbs, nil
}

func resolveAccessGrantAudience(r *http.Request, st store.Store, raw string) (subjectKind, subjectID, display string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", fmt.Errorf("audience required")
	}
	if raw == "admin" {
		return networkauth.SubjectKindClient, "admin", "admin", nil
	}
	if strings.HasPrefix(raw, "github:") || strings.HasPrefix(raw, "oidc:") || strings.HasPrefix(raw, "user:") {
		return networkauth.SubjectKindClient, raw, raw, nil
	}

	type candidate struct {
		id      string
		display string
	}
	var matches []candidate

	user, err := st.UserGetByUsername(r.Context(), raw)
	if err != nil {
		return "", "", "", fmt.Errorf("resolving audience: %w", err)
	}
	if user != nil {
		matches = append(matches, candidate{
			id:      fmt.Sprintf("github:%d", user.GitHubID),
			display: user.Username,
		})
	}

	oidcUsers, err := st.OIDCUserListByUsername(r.Context(), raw)
	if err != nil {
		return "", "", "", fmt.Errorf("resolving audience: %w", err)
	}
	for _, user := range oidcUsers {
		matches = append(matches, candidate{
			id:      "oidc:" + user.Sub,
			display: user.Username,
		})
	}

	switch len(matches) {
	case 0:
		return "", "", "", fmt.Errorf("principal %q not found", raw)
	case 1:
		return networkauth.SubjectKindClient, matches[0].id, matches[0].display, nil
	default:
		return "", "", "", fmt.Errorf("principal %q is ambiguous; use an explicit subject like github:<id> or oidc:<sub>", raw)
	}
}
