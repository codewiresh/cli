package relay

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/codewiresh/codewire/internal/oauth"
	"github.com/codewiresh/codewire/internal/store"
)

// RelayConfig configures the relay server.
type RelayConfig struct {
	// BaseURL is the public-facing HTTPS URL of the relay.
	BaseURL string
	// ListenAddr is the HTTP listen address (default ":8080").
	ListenAddr string
	// SSHListenAddr is the SSH listen address (default ":2222").
	SSHListenAddr string
	// DataDir is where relay.db lives.
	DataDir string
	// AuthMode controls authentication: "oidc", "github", "token", "none".
	AuthMode string
	// AuthToken is the shared secret when AuthMode is "token" or as fallback.
	AuthToken string
	// AllowedUsers is a list of GitHub usernames allowed to authenticate.
	AllowedUsers []string
	// GitHubClientID is a manual override for GitHub OAuth App client ID.
	GitHubClientID string
	// GitHubClientSecret is a manual override for GitHub OAuth App client secret.
	GitHubClientSecret string
	// OIDCIssuer is the OIDC provider issuer URL (e.g. https://auth.codewire.sh).
	// Required when AuthMode is "oidc".
	OIDCIssuer string
	// OIDCClientID is the registered OIDC client ID.
	OIDCClientID string
	// OIDCClientSecret is the registered OIDC client secret.
	OIDCClientSecret string
	// OIDCAllowedGroups restricts access to members of these groups.
	// Empty means any authenticated user is allowed.
	OIDCAllowedGroups []string
}

const defaultFleetID = "default"

// RunRelay starts the relay server. It blocks until ctx is cancelled.
func RunRelay(ctx context.Context, cfg RelayConfig) error {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.SSHListenAddr == "" {
		cfg.SSHListenAddr = ":2222"
	}

	st, err := store.NewSQLiteStore(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer st.Close()

	hub := NewNodeHub()
	sessions := NewPendingSessions()

	sshSrv, err := NewSSHServer(st, hub, sessions)
	if err != nil {
		return fmt.Errorf("creating SSH server: %w", err)
	}

	// Start SSH listener.
	sshLn, err := net.Listen("tcp", cfg.SSHListenAddr)
	if err != nil {
		return fmt.Errorf("SSH listen: %w", err)
	}
	go sshSrv.Serve(ctx, sshLn)
	fmt.Fprintf(os.Stderr, "[relay] SSH listening on %s\n", cfg.SSHListenAddr)

	// Build HTTP mux.
	mux := buildMux(hub, sessions, st, cfg)

	httpSrv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr, "[relay] HTTP listening on %s (base_url=%s)\n", cfg.ListenAddr, cfg.BaseURL)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// BuildRelayMux creates an HTTP mux with node agent endpoints (no OAuth, no GitHub).
// Used in tests; RunRelay calls the full buildMux.
func BuildRelayMux(hub *NodeHub, sessions *PendingSessions, st store.Store) http.Handler {
	mux := http.NewServeMux()
	RegisterNodeConnectHandler(mux, hub, st)
	RegisterBackHandler(mux, sessions, st)
	return mux
}

func buildMux(hub *NodeHub, sessions *PendingSessions, st store.Store, cfg RelayConfig) *http.ServeMux {
	authMiddleware := oauth.RequireAuth(st, cfg.AuthToken)
	joinRL := newRateLimiter(10, time.Minute)

	mux := http.NewServeMux()

	// Node agent WebSocket endpoints.
	RegisterNodeConnectHandler(mux, hub, st)
	RegisterBackHandler(mux, sessions, st)

	// GitHub OAuth (when AuthMode == "github").
	if cfg.AuthMode == "github" {
		mux.HandleFunc("GET /auth/github/manifest/callback", oauth.ManifestCallbackHandler(st, cfg.BaseURL))
		mux.HandleFunc("GET /auth/github", oauth.LoginHandler(st, cfg.BaseURL, cfg.AllowedUsers))
		mux.HandleFunc("GET /auth/github/callback", oauth.CallbackHandler(st, cfg.BaseURL, cfg.AllowedUsers))
		mux.HandleFunc("GET /auth/session", oauth.SessionInfoHandler(st))
		mux.HandleFunc("GET /{$}", oauth.SetupPageHandler(st, cfg.BaseURL))

		if cfg.GitHubClientID != "" && cfg.GitHubClientSecret != "" {
			existing, _ := st.GitHubAppGet(context.Background())
			if existing == nil {
				st.GitHubAppSet(context.Background(), store.GitHubApp{
					ClientID:     cfg.GitHubClientID,
					ClientSecret: cfg.GitHubClientSecret,
					Owner:        "manual",
					CreatedAt:    time.Now().UTC(),
				})
			}
		}
	}

	// OIDC auth (when AuthMode == "oidc").
	if cfg.AuthMode == "oidc" {
		oidcProvider := &oauth.OIDCProvider{
			Issuer:        cfg.OIDCIssuer,
			ClientID:      cfg.OIDCClientID,
			ClientSecret:  cfg.OIDCClientSecret,
			AllowedGroups: cfg.OIDCAllowedGroups,
		}
		if err := oidcProvider.Discover(context.Background()); err != nil {
			// Log but don't crash — relay will return errors on auth endpoints if discovery failed.
			fmt.Fprintf(os.Stderr, "[relay] OIDC discovery failed: %v\n", err)
		}
		mux.HandleFunc("GET /auth/oidc", oidcProvider.LoginHandler(st, cfg.BaseURL))
		mux.HandleFunc("GET /auth/oidc/callback", oidcProvider.CallbackHandler(st, cfg.BaseURL))
		mux.HandleFunc("GET /auth/session", oidcProvider.OIDCSessionInfoHandler(st))
		mux.HandleFunc("GET /{$}", oidcProvider.OIDCIndexHandler(cfg.BaseURL))

		// Device flow (public, rate-limited same as join).
		mux.HandleFunc("POST /api/v1/device/authorize", rateLimitMiddleware(joinRL, deviceAuthorizeHandler(st, oidcProvider)))
		mux.HandleFunc("POST /api/v1/device/poll", devicePollHandler(st, oidcProvider))
	}

	// Auth config discovery (unauthenticated, used by cw setup).
	mux.HandleFunc("GET /api/v1/auth/config", authConfigHandler(cfg.AuthMode))

	// Node registration (issues a random node token).
	mux.Handle("GET /api/v1/networks", authMiddleware(http.HandlerFunc(networkListHandler(st))))
	mux.Handle("POST /api/v1/networks", authMiddleware(http.HandlerFunc(networkCreateHandler(st))))
	mux.Handle("POST /api/v1/nodes", authMiddleware(http.HandlerFunc(nodeRegisterHandler(st))))
	mux.Handle("DELETE /api/v1/nodes/{name}", authMiddleware(http.HandlerFunc(nodeRevokeHandler(st))))
	mux.Handle("GET /api/v1/nodes", authMiddleware(http.HandlerFunc(nodesListHandler(st))))

	// Invite management (admin-only).
	mux.Handle("POST /api/v1/invites", authMiddleware(http.HandlerFunc(inviteCreateHandler(st))))
	mux.Handle("GET /api/v1/invites", authMiddleware(http.HandlerFunc(inviteListHandler(st))))
	mux.Handle("DELETE /api/v1/invites/{token}", authMiddleware(http.HandlerFunc(inviteDeleteHandler(st))))

	// Invite redemption (public, rate-limited).
	mux.HandleFunc("POST /api/v1/join", rateLimitMiddleware(joinRL, joinHandler(st)))
	mux.HandleFunc("GET /join", joinPageHandler(cfg.BaseURL))

	// KV API.
	mux.Handle("PUT /api/v1/kv/{namespace}/{key}", authMiddleware(http.HandlerFunc(kvSetHandler(st))))
	mux.Handle("GET /api/v1/kv/{namespace}/{key}", authMiddleware(http.HandlerFunc(kvGetHandler(st))))
	mux.Handle("DELETE /api/v1/kv/{namespace}/{key}", authMiddleware(http.HandlerFunc(kvDeleteHandler(st))))
	mux.Handle("GET /api/v1/kv/{namespace}", authMiddleware(http.HandlerFunc(kvListHandler(st))))

	// Health check.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	return mux
}

func resolveFleetID(raw string) string {
	fleetID := strings.TrimSpace(raw)
	if fleetID == "" {
		return defaultFleetID
	}
	return fleetID
}

func validateNetworkID(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("network id required")
	}
	for _, ch := range raw {
		isLetter := ch >= 'a' && ch <= 'z'
		isUpper := ch >= 'A' && ch <= 'Z'
		isDigit := ch >= '0' && ch <= '9'
		if isLetter || isUpper || isDigit || ch == '-' || ch == '_' {
			continue
		}
		return fmt.Errorf("network id may only contain letters, numbers, '-' or '_'")
	}
	return nil
}

// --- Networks ---

type networkResponse struct {
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	NodeCount   int       `json:"node_count"`
	InviteCount int       `json:"invite_count"`
}

func networkListHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		networks, err := st.NetworkList(r.Context())
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		resp := make([]networkResponse, 0, len(networks))
		for _, network := range networks {
			resp = append(resp, networkResponse{
				ID:          network.ID,
				CreatedAt:   network.CreatedAt,
				NodeCount:   network.NodeCount,
				InviteCount: network.InviteCount,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func networkCreateHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			NetworkID string `json:"network_id"`
			FleetID   string `json:"fleet_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "network_id required", http.StatusBadRequest)
			return
		}

		networkID := strings.TrimSpace(req.NetworkID)
		if networkID == "" {
			networkID = strings.TrimSpace(req.FleetID)
		}
		networkID = resolveFleetID(networkID)
		if err := validateNetworkID(networkID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := st.NetworkEnsure(r.Context(), networkID); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":     "created",
			"network_id": networkID,
			"fleet_id":   networkID,
		})
	}
}

// --- Node Registration ---

func nodeRegisterHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			NodeName string `json:"node_name"`
			FleetID  string `json:"fleet_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeName == "" {
			http.Error(w, "node_name required", http.StatusBadRequest)
			return
		}
		fleetID := resolveFleetID(req.FleetID)

		token := generateToken()

		auth := oauth.GetAuth(r.Context())
		var githubID *int64
		if auth != nil && auth.UserID != 0 {
			githubID = &auth.UserID
		}

		node := store.NodeRecord{
			FleetID:      fleetID,
			Name:         req.NodeName,
			Token:        token,
			GitHubID:     githubID,
			AuthorizedAt: time.Now().UTC(),
			LastSeenAt:   time.Now().UTC(),
		}
		if err := st.NodeRegister(r.Context(), node); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":     "registered",
			"node_token": token,
			"node_name":  req.NodeName,
			"fleet_id":   fleetID,
		})
	}
}

// --- Node Revocation ---

func nodeRevokeHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		fleetID := resolveFleetID(r.URL.Query().Get("fleet_id"))

		node, err := st.NodeGet(r.Context(), fleetID, name)
		if err != nil || node == nil {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}

		if err := st.NodeDelete(r.Context(), fleetID, name); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "revoked",
			"node":   name,
		})
	}
}

// --- Node Discovery ---

type nodeResponse struct {
	FleetID   string `json:"fleet_id,omitempty"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

func nodesListHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		all := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("all")), "true")

		var (
			fleetID string
			nodes   []store.NodeRecord
			err     error
		)
		if all {
			nodes, err = st.NodeListAll(r.Context())
		} else {
			fleetID = resolveFleetID(r.URL.Query().Get("fleet_id"))
			nodes, err = st.NodeList(r.Context(), fleetID)
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		resp := make([]nodeResponse, 0, len(nodes))
		for _, n := range nodes {
			connected := time.Since(n.LastSeenAt) < 2*time.Minute
			resp = append(resp, nodeResponse{
				FleetID:   n.FleetID,
				Name:      n.Name,
				Connected: connected,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// --- Invite Handlers ---

type inviteCreateRequest struct {
	FleetID string `json:"fleet_id,omitempty"`
	Uses    int    `json:"uses"`
	TTL     string `json:"ttl"`
}

func inviteCreateHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req inviteCreateRequest
		json.NewDecoder(r.Body).Decode(&req)

		if req.Uses <= 0 {
			req.Uses = 1
		}
		fleetID := resolveFleetID(req.FleetID)

		ttl := time.Hour
		if req.TTL != "" {
			parsed, err := time.ParseDuration(req.TTL)
			if err != nil {
				http.Error(w, "invalid ttl", http.StatusBadRequest)
				return
			}
			ttl = parsed
		}

		auth := oauth.GetAuth(r.Context())
		var createdBy *int64
		if auth != nil && auth.UserID != 0 {
			createdBy = &auth.UserID
		}

		now := time.Now().UTC()
		invite := store.Invite{
			FleetID:       fleetID,
			Token:         oauth.GenerateInviteToken(),
			CreatedBy:     createdBy,
			UsesRemaining: req.Uses,
			ExpiresAt:     now.Add(ttl),
			CreatedAt:     now,
		}

		if err := st.InviteCreate(r.Context(), invite); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(invite)
	}
}

func inviteListHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fleetID := resolveFleetID(r.URL.Query().Get("fleet_id"))
		invites, err := st.InviteList(r.Context(), fleetID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(invites)
	}
}

func inviteDeleteHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		fleetID := resolveFleetID(r.URL.Query().Get("fleet_id"))
		if err := st.InviteDelete(r.Context(), fleetID, token); err != nil {
			http.Error(w, "invite not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Invite Redemption ---

type joinRequest struct {
	NodeName    string `json:"node_name"`
	InviteToken string `json:"invite_token"`
}

func joinHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req joinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if req.NodeName == "" || req.InviteToken == "" {
			http.Error(w, "node_name and invite_token required", http.StatusBadRequest)
			return
		}

		// Look up invite before consuming (for github_id association).
		invite, _ := st.InviteGet(r.Context(), req.InviteToken)

		// Consume invite (validates + decrements uses).
		if err := st.InviteConsume(r.Context(), req.InviteToken); err != nil {
			http.Error(w, "invalid or expired invite", http.StatusForbidden)
			return
		}

		var githubID *int64
		fleetID := defaultFleetID
		if invite != nil && invite.CreatedBy != nil {
			githubID = invite.CreatedBy
		}
		if invite != nil && invite.FleetID != "" {
			fleetID = invite.FleetID
		}

		token := generateToken()
		node := store.NodeRecord{
			FleetID:      fleetID,
			Name:         req.NodeName,
			Token:        token,
			GitHubID:     githubID,
			AuthorizedAt: time.Now().UTC(),
			LastSeenAt:   time.Now().UTC(),
		}

		if err := st.NodeRegister(r.Context(), node); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":     "registered",
			"node_token": token,
			"node_name":  req.NodeName,
			"fleet_id":   fleetID,
		})
	}
}

func joinPageHandler(baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		invite := r.URL.Query().Get("invite")
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Join CodeWire Relay</title>
<style>body{font-family:system-ui;max-width:480px;margin:80px auto;text-align:center;color:#1a1a1a}
h2{font-weight:600}
.code{font-family:monospace;background:#f5f5f5;padding:8px 16px;border-radius:6px;display:inline-block;margin:12px 0;word-break:break-all}
p{color:#525252;line-height:1.6}
</style></head><body>
<h2>Join CodeWire Relay</h2>
<p>Use this invite code to register your device:</p>
<div class="code">%s</div>
<p>Run on your device:</p>
<div class="code">cw relay setup %s %s</div>
</body></html>`, invite, baseURL, invite)
	}
}

// --- KV API ---

func kvSetHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ns := r.PathValue("namespace")
		key := r.PathValue("key")
		fleetID := resolveFleetID(r.URL.Query().Get("fleet_id"))

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var ttl *time.Duration
		if ttlStr := r.Header.Get("X-TTL"); ttlStr != "" {
			d, err := time.ParseDuration(ttlStr)
			if err != nil {
				http.Error(w, "invalid X-TTL header", http.StatusBadRequest)
				return
			}
			ttl = &d
		}

		if err := st.KVSet(r.Context(), fleetID, ns, key, body, ttl); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func kvGetHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ns := r.PathValue("namespace")
		key := r.PathValue("key")
		fleetID := resolveFleetID(r.URL.Query().Get("fleet_id"))

		val, err := st.KVGet(r.Context(), fleetID, ns, key)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if val == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(val)
	}
}

func kvDeleteHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ns := r.PathValue("namespace")
		key := r.PathValue("key")
		fleetID := resolveFleetID(r.URL.Query().Get("fleet_id"))

		if err := st.KVDelete(r.Context(), fleetID, ns, key); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func kvListHandler(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ns := r.PathValue("namespace")
		prefix := r.URL.Query().Get("prefix")
		fleetID := resolveFleetID(r.URL.Query().Get("fleet_id"))

		entries, err := st.KVList(r.Context(), fleetID, ns, prefix)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}
}

// --- Rate Limiter ---

type rateLimiter struct {
	mu      sync.Mutex
	entries map[string][]time.Time
	limit   int
	window  time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		entries: make(map[string][]time.Time),
		limit:   limit,
		window:  window,
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	times := rl.entries[ip]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.limit {
		rl.entries[ip] = valid
		return false
	}
	rl.entries[ip] = append(valid, now)
	return true
}

func remoteIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func rateLimitMiddleware(rl *rateLimiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(remoteIP(r)) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// --- Auth Config ---

func authConfigHandler(authMode string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"auth_mode": authMode,
		})
	}
}

// --- Helpers ---

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
