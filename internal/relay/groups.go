package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/codewiresh/codewire/internal/oauth"
	"github.com/codewiresh/codewire/internal/store"
)

type groupCreateRequest struct {
	NetworkID string `json:"network_id"`
	Name      string `json:"name"`
}

type groupMemberRequest struct {
	NetworkID   string `json:"network_id"`
	NodeName    string `json:"node_name"`
	SessionName string `json:"session_name"`
}

type groupPolicyRequest struct {
	NetworkID      string `json:"network_id"`
	MessagesPolicy string `json:"messages_policy"`
	DebugPolicy    string `json:"debug_policy"`
}

type groupResponse struct {
	NetworkID string              `json:"network_id"`
	Name      string              `json:"name"`
	CreatedAt time.Time           `json:"created_at"`
	CreatedBy string              `json:"created_by,omitempty"`
	Members   []store.GroupMember `json:"members,omitempty"`
	Policy    *store.GroupPolicy  `json:"policy,omitempty"`
}

func groupsCreateHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity := oauth.GetAuth(r.Context())
		if identity == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req groupCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		networkID, err := requiredNetworkID(req.NetworkID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateGroupName(req.Name); err != nil {
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

		createdBy, err := membershipSubject(identity)
		if err != nil && !identity.IsAdmin {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		now := time.Now().UTC()
		group := store.Group{
			NetworkID: networkID,
			Name:      strings.TrimSpace(req.Name),
			CreatedAt: now,
			CreatedBy: createdBy,
		}
		if err := st.GroupCreate(r.Context(), group); err != nil {
			if err.Error() == "group already exists" {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		policy, err := st.GroupPolicyGet(r.Context(), networkID, group.Name)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(groupResponse{
			NetworkID: group.NetworkID,
			Name:      group.Name,
			CreatedAt: group.CreatedAt,
			CreatedBy: group.CreatedBy,
			Policy:    policy,
		})
	}
}

func groupsListHandler(st store.Store) http.HandlerFunc {
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

		groups, err := st.GroupList(r.Context(), networkID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		resp := make([]groupResponse, 0, len(groups))
		for _, group := range groups {
			policy, err := st.GroupPolicyGet(r.Context(), networkID, group.Name)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			resp = append(resp, groupResponse{
				NetworkID: group.NetworkID,
				Name:      group.Name,
				CreatedAt: group.CreatedAt,
				CreatedBy: group.CreatedBy,
				Policy:    policy,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func groupBindingsHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodeName := strings.TrimSpace(r.URL.Query().Get("node_name"))
		sessionName := strings.TrimSpace(r.URL.Query().Get("session_name"))
		if sessionName == "" {
			http.Error(w, "session_name required", http.StatusBadRequest)
			return
		}

		var networkID string
		if node, err := nodeAuthFromRequest(r, st); err == nil && node != nil {
			networkID = node.NetworkID
			if nodeName == "" {
				nodeName = node.Name
			}
			if nodeName != node.Name {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		} else {
			identity := oauth.GetAuth(r.Context())
			if identity == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var err error
			networkID, err = requiredNetworkID(r.URL.Query().Get("network_id"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if nodeName == "" {
				http.Error(w, "node_name required", http.StatusBadRequest)
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
		}

		bindings, err := st.GroupBindingsForSession(r.Context(), networkID, nodeName, sessionName)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bindings)
	}
}

func groupsGetHandler(st store.Store) http.HandlerFunc {
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
		groupName := strings.TrimSpace(r.PathValue("name"))
		if err := validateGroupName(groupName); err != nil {
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

		resp, ok, err := loadGroupResponse(r, st, networkID, groupName)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "group not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func groupsDeleteHandler(st store.Store) http.HandlerFunc {
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
		groupName := strings.TrimSpace(r.PathValue("name"))
		if err := validateGroupName(groupName); err != nil {
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

		if err := st.GroupDelete(r.Context(), networkID, groupName); err != nil {
			if err.Error() == "group not found" {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":     "deleted",
			"network_id": networkID,
			"name":       groupName,
		})
	}
}

func groupMembersAddHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groupName := strings.TrimSpace(r.PathValue("name"))
		if err := validateGroupName(groupName); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var req groupMemberRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		nodeName := strings.TrimSpace(req.NodeName)
		sessionName := strings.TrimSpace(req.SessionName)
		if sessionName == "" {
			http.Error(w, "session_name required", http.StatusBadRequest)
			return
		}

		var networkID string
		if node, err := nodeAuthFromRequest(r, st); err == nil && node != nil {
			networkID = node.NetworkID
			if nodeName == "" {
				nodeName = node.Name
			}
			if nodeName != node.Name {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		} else {
			identity := oauth.GetAuth(r.Context())
			if identity == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var err error
			networkID, err = requiredNetworkID(req.NetworkID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if nodeName == "" {
				http.Error(w, "node_name required", http.StatusBadRequest)
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
		}

		member := store.GroupMember{
			NetworkID:   networkID,
			GroupName:   groupName,
			NodeName:    nodeName,
			SessionName: sessionName,
			CreatedAt:   time.Now().UTC(),
		}
		if err := st.GroupMemberAdd(r.Context(), member); err != nil {
			if err.Error() == "group not found" {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(member)
	}
}

func groupMembersRemoveHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groupName := strings.TrimSpace(r.PathValue("name"))
		if err := validateGroupName(groupName); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		nodeName := strings.TrimSpace(r.URL.Query().Get("node_name"))
		sessionName := strings.TrimSpace(r.URL.Query().Get("session_name"))
		if sessionName == "" {
			http.Error(w, "session_name required", http.StatusBadRequest)
			return
		}

		var networkID string
		if node, err := nodeAuthFromRequest(r, st); err == nil && node != nil {
			networkID = node.NetworkID
			if nodeName == "" {
				nodeName = node.Name
			}
			if nodeName != node.Name {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		} else {
			identity := oauth.GetAuth(r.Context())
			if identity == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var err error
			networkID, err = requiredNetworkID(r.URL.Query().Get("network_id"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if nodeName == "" {
				http.Error(w, "node_name required", http.StatusBadRequest)
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
		}

		if err := st.GroupMemberRemove(r.Context(), networkID, groupName, nodeName, sessionName); err != nil {
			if err.Error() == "group member not found" {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":       "removed",
			"network_id":   networkID,
			"group_name":   groupName,
			"node_name":    nodeName,
			"session_name": sessionName,
		})
	}
}

func groupPolicySetHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity := oauth.GetAuth(r.Context())
		if identity == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		groupName := strings.TrimSpace(r.PathValue("name"))
		if err := validateGroupName(groupName); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var req groupPolicyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		networkID, err := requiredNetworkID(req.NetworkID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		messagesPolicy, err := normalizeGroupMessagesPolicy(req.MessagesPolicy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		debugPolicy, err := normalizeGroupDebugPolicy(req.DebugPolicy)
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

		policy := store.GroupPolicy{
			NetworkID:      networkID,
			GroupName:      groupName,
			MessagesPolicy: messagesPolicy,
			DebugPolicy:    debugPolicy,
			UpdatedAt:      time.Now().UTC(),
		}
		if err := st.GroupPolicySet(r.Context(), policy); err != nil {
			if err.Error() == "group not found" {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(policy)
	}
}

func loadGroupResponse(r *http.Request, st store.Store, networkID, groupName string) (groupResponse, bool, error) {
	group, err := st.GroupGet(r.Context(), networkID, groupName)
	if err != nil {
		return groupResponse{}, false, err
	}
	if group == nil {
		return groupResponse{}, false, nil
	}
	members, err := st.GroupMemberList(r.Context(), networkID, groupName)
	if err != nil {
		return groupResponse{}, false, err
	}
	policy, err := st.GroupPolicyGet(r.Context(), networkID, groupName)
	if err != nil {
		return groupResponse{}, false, err
	}
	return groupResponse{
		NetworkID: group.NetworkID,
		Name:      group.Name,
		CreatedAt: group.CreatedAt,
		CreatedBy: group.CreatedBy,
		Members:   members,
		Policy:    policy,
	}, true, nil
}

func validateGroupName(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("group name required")
	}
	for _, ch := range raw {
		isLetter := ch >= 'a' && ch <= 'z'
		isUpper := ch >= 'A' && ch <= 'Z'
		isDigit := ch >= '0' && ch <= '9'
		if isLetter || isUpper || isDigit || ch == '-' || ch == '_' {
			continue
		}
		return fmt.Errorf("group name may only contain letters, numbers, '-' or '_'")
	}
	return nil
}

func normalizeGroupMessagesPolicy(raw string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", store.GroupMessagesInternalOnly:
		return store.GroupMessagesInternalOnly, nil
	case store.GroupMessagesOpen:
		return store.GroupMessagesOpen, nil
	default:
		return "", fmt.Errorf("invalid messages_policy")
	}
}

func normalizeGroupDebugPolicy(raw string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", store.GroupDebugObserveOnly:
		return store.GroupDebugObserveOnly, nil
	case store.GroupDebugNone:
		return store.GroupDebugNone, nil
	case store.GroupDebugFull:
		return store.GroupDebugFull, nil
	default:
		return "", fmt.Errorf("invalid debug_policy")
	}
}
