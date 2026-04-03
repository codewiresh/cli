package networkauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const maxClockSkew = 30 * time.Second

// ResolveNetworkID normalizes whitespace in a network ID.
func ResolveNetworkID(raw string) string {
	return strings.TrimSpace(raw)
}

// NewIssuerState generates a new Ed25519 issuer state for one network.
func NewIssuerState(networkID string) (*IssuerState, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating ed25519 key: %w", err)
	}

	now := time.Now().UTC()
	return &IssuerState{
		NetworkID:  ResolveNetworkID(networkID),
		Issuer:     "relay:" + ResolveNetworkID(networkID),
		KeyID:      randomID("kid_"),
		PrivateKey: base64.RawURLEncoding.EncodeToString(privateKey),
		PublicKey:  base64.RawURLEncoding.EncodeToString(publicKey),
		CreatedAt:  now,
		Version:    1,
	}, nil
}

// Bundle returns the verifier bundle for the issuer state.
func (s *IssuerState) Bundle(now time.Time, validity time.Duration) *VerifierBundle {
	if validity <= 0 {
		validity = DefaultBundleValidity
	}
	return &VerifierBundle{
		NetworkID: ResolveNetworkID(s.NetworkID),
		Issuer:    s.Issuer,
		Keys: []VerifierKey{{
			KeyID:     s.KeyID,
			Algorithm: AlgorithmEd25519,
			PublicKey: s.PublicKey,
		}},
		Version:   s.Version,
		ExpiresAt: now.UTC().Add(validity),
	}
}

// SignRuntimeCredential signs a short-lived runtime credential.
func SignRuntimeCredential(state *IssuerState, subjectKind, subjectID string, now time.Time, ttl time.Duration) (string, *RuntimeClaims, error) {
	if state == nil {
		return "", nil, fmt.Errorf("issuer state is nil")
	}
	if strings.TrimSpace(subjectKind) == "" {
		return "", nil, fmt.Errorf("subject kind is required")
	}
	if strings.TrimSpace(subjectID) == "" {
		return "", nil, fmt.Errorf("subject id is required")
	}
	if ttl <= 0 {
		ttl = DefaultRuntimeTTL
	}

	privateKeyBytes, err := base64.RawURLEncoding.DecodeString(state.PrivateKey)
	if err != nil {
		return "", nil, fmt.Errorf("decoding private key: %w", err)
	}
	privateKey := ed25519.PrivateKey(privateKeyBytes)

	claims := &RuntimeClaims{
		Kind:        RuntimeKind,
		NetworkID:   ResolveNetworkID(state.NetworkID),
		SubjectKind: subjectKind,
		SubjectID:   subjectID,
		Issuer:      state.Issuer,
		KeyID:       state.KeyID,
		IssuedAt:    now.UTC(),
		ExpiresAt:   now.UTC().Add(ttl),
		JTI:         randomID("rt_"),
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", nil, fmt.Errorf("marshalling runtime claims: %w", err)
	}
	signature := ed25519.Sign(privateKey, payload)
	token := strings.Join([]string{
		RuntimeTokenPrefix,
		base64.RawURLEncoding.EncodeToString(payload),
		base64.RawURLEncoding.EncodeToString(signature),
	}, ".")
	return token, claims, nil
}

// VerifyRuntimeCredential verifies a signed runtime credential from a verifier bundle.
func VerifyRuntimeCredential(token string, bundle *VerifierBundle, now time.Time) (*RuntimeClaims, error) {
	if bundle == nil {
		return nil, fmt.Errorf("verifier bundle is nil")
	}

	claims, payload, signature, err := parseRuntimeCredential(token)
	if err != nil {
		return nil, err
	}
	if claims.Kind != RuntimeKind {
		return nil, fmt.Errorf("unexpected runtime token kind %q", claims.Kind)
	}
	if claims.NetworkID != ResolveNetworkID(bundle.NetworkID) {
		return nil, fmt.Errorf("runtime token network_id = %q, want %q", claims.NetworkID, ResolveNetworkID(bundle.NetworkID))
	}
	if now.UTC().After(bundle.ExpiresAt.Add(maxClockSkew)) {
		return nil, fmt.Errorf("verifier bundle expired")
	}
	if now.UTC().Before(claims.IssuedAt.Add(-maxClockSkew)) {
		return nil, fmt.Errorf("runtime token issued in the future")
	}
	if now.UTC().After(claims.ExpiresAt.Add(maxClockSkew)) {
		return nil, fmt.Errorf("runtime token expired")
	}

	var publicKey ed25519.PublicKey
	for _, key := range bundle.Keys {
		if key.KeyID != claims.KeyID {
			continue
		}
		if key.Algorithm != AlgorithmEd25519 {
			return nil, fmt.Errorf("unsupported verifier algorithm %q", key.Algorithm)
		}
		keyBytes, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("decoding verifier public key: %w", err)
		}
		publicKey = ed25519.PublicKey(keyBytes)
		break
	}
	if len(publicKey) == 0 {
		return nil, fmt.Errorf("no verifier key for kid %q", claims.KeyID)
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return nil, fmt.Errorf("invalid runtime token signature")
	}
	return claims, nil
}

// ParseRuntimeCredential decodes runtime credential claims without verifying
// the signature. Callers must verify the token separately before trusting the
// returned values.
func ParseRuntimeCredential(token string) (*RuntimeClaims, error) {
	claims, _, _, err := parseRuntimeCredential(token)
	return claims, err
}

func parseRuntimeCredential(token string) (*RuntimeClaims, []byte, []byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != RuntimeTokenPrefix {
		return nil, nil, nil, fmt.Errorf("invalid runtime token format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decoding runtime token payload: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decoding runtime token signature: %w", err)
	}

	var claims RuntimeClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, nil, nil, fmt.Errorf("parsing runtime token payload: %w", err)
	}
	return &claims, payload, signature, nil
}

// SignSenderDelegation signs a short-lived session sender delegation.
func SignSenderDelegation(state *IssuerState, sourceNode string, fromSessionID *uint32, fromSessionName string, sourceGroups []string, verbs []string, audienceNode string, now time.Time, ttl time.Duration) (string, *SenderDelegationClaims, error) {
	if state == nil {
		return "", nil, fmt.Errorf("issuer state is nil")
	}
	if strings.TrimSpace(sourceNode) == "" {
		return "", nil, fmt.Errorf("source node is required")
	}
	if fromSessionID == nil && strings.TrimSpace(fromSessionName) == "" {
		return "", nil, fmt.Errorf("sender delegation requires session id or name")
	}
	if len(verbs) == 0 {
		return "", nil, fmt.Errorf("sender delegation requires at least one verb")
	}
	if ttl <= 0 {
		ttl = DefaultSenderTTL
	}

	privateKeyBytes, err := base64.RawURLEncoding.DecodeString(state.PrivateKey)
	if err != nil {
		return "", nil, fmt.Errorf("decoding private key: %w", err)
	}
	privateKey := ed25519.PrivateKey(privateKeyBytes)

	copiedVerbs := append([]string(nil), verbs...)
	copiedGroups := append([]string(nil), sourceGroups...)
	claims := &SenderDelegationClaims{
		Kind:            SenderDelegationKind,
		NetworkID:       ResolveNetworkID(state.NetworkID),
		SourceNode:      strings.TrimSpace(sourceNode),
		FromSessionID:   fromSessionID,
		FromSessionName: strings.TrimSpace(fromSessionName),
		SourceGroups:    copiedGroups,
		Verbs:           copiedVerbs,
		AudienceNode:    strings.TrimSpace(audienceNode),
		Issuer:          state.Issuer,
		KeyID:           state.KeyID,
		IssuedAt:        now.UTC(),
		ExpiresAt:       now.UTC().Add(ttl),
		JTI:             randomID("sd_"),
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", nil, fmt.Errorf("marshalling sender delegation claims: %w", err)
	}
	signature := ed25519.Sign(privateKey, payload)
	token := strings.Join([]string{
		SenderTokenPrefix,
		base64.RawURLEncoding.EncodeToString(payload),
		base64.RawURLEncoding.EncodeToString(signature),
	}, ".")
	return token, claims, nil
}

// VerifySenderDelegation verifies a signed sender delegation from a verifier bundle.
func VerifySenderDelegation(token string, bundle *VerifierBundle, now time.Time) (*SenderDelegationClaims, error) {
	if bundle == nil {
		return nil, fmt.Errorf("verifier bundle is nil")
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != SenderTokenPrefix {
		return nil, fmt.Errorf("invalid sender delegation format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding sender delegation payload: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decoding sender delegation signature: %w", err)
	}

	var claims SenderDelegationClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parsing sender delegation payload: %w", err)
	}
	if claims.Kind != SenderDelegationKind {
		return nil, fmt.Errorf("unexpected sender delegation kind %q", claims.Kind)
	}
	if claims.NetworkID != ResolveNetworkID(bundle.NetworkID) {
		return nil, fmt.Errorf("sender delegation network_id = %q, want %q", claims.NetworkID, ResolveNetworkID(bundle.NetworkID))
	}
	if now.UTC().After(bundle.ExpiresAt.Add(maxClockSkew)) {
		return nil, fmt.Errorf("verifier bundle expired")
	}
	if now.UTC().Before(claims.IssuedAt.Add(-maxClockSkew)) {
		return nil, fmt.Errorf("sender delegation issued in the future")
	}
	if now.UTC().After(claims.ExpiresAt.Add(maxClockSkew)) {
		return nil, fmt.Errorf("sender delegation expired")
	}

	var publicKey ed25519.PublicKey
	for _, key := range bundle.Keys {
		if key.KeyID != claims.KeyID {
			continue
		}
		if key.Algorithm != AlgorithmEd25519 {
			return nil, fmt.Errorf("unsupported verifier algorithm %q", key.Algorithm)
		}
		keyBytes, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("decoding verifier public key: %w", err)
		}
		publicKey = ed25519.PublicKey(keyBytes)
		break
	}
	if len(publicKey) == 0 {
		return nil, fmt.Errorf("no verifier key for kid %q", claims.KeyID)
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return nil, fmt.Errorf("invalid sender delegation signature")
	}
	return &claims, nil
}

// SignObserverDelegation signs a short-lived remote read/listen grant.
func SignObserverDelegation(state *IssuerState, targetNode string, sessionID *uint32, sessionName string, verbs []string, audienceSubjectKind, audienceSubjectID string, now time.Time, ttl time.Duration) (string, *ObserverDelegationClaims, error) {
	if state == nil {
		return "", nil, fmt.Errorf("issuer state is nil")
	}
	if strings.TrimSpace(targetNode) == "" {
		return "", nil, fmt.Errorf("target node is required")
	}
	if sessionID == nil && strings.TrimSpace(sessionName) == "" {
		return "", nil, fmt.Errorf("observer delegation requires session id or name")
	}
	if len(verbs) == 0 {
		return "", nil, fmt.Errorf("observer delegation requires at least one verb")
	}
	if strings.TrimSpace(audienceSubjectKind) == "" {
		return "", nil, fmt.Errorf("audience subject kind is required")
	}
	if strings.TrimSpace(audienceSubjectID) == "" {
		return "", nil, fmt.Errorf("audience subject id is required")
	}
	if ttl <= 0 {
		ttl = DefaultObserverTTL
	}

	privateKeyBytes, err := base64.RawURLEncoding.DecodeString(state.PrivateKey)
	if err != nil {
		return "", nil, fmt.Errorf("decoding private key: %w", err)
	}
	privateKey := ed25519.PrivateKey(privateKeyBytes)

	copiedVerbs := append([]string(nil), verbs...)
	claims := &ObserverDelegationClaims{
		Kind:                ObserverDelegationKind,
		NetworkID:           ResolveNetworkID(state.NetworkID),
		TargetNode:          strings.TrimSpace(targetNode),
		SessionID:           sessionID,
		SessionName:         strings.TrimSpace(sessionName),
		Verbs:               copiedVerbs,
		AudienceSubjectKind: strings.TrimSpace(audienceSubjectKind),
		AudienceSubjectID:   strings.TrimSpace(audienceSubjectID),
		Issuer:              state.Issuer,
		KeyID:               state.KeyID,
		IssuedAt:            now.UTC(),
		ExpiresAt:           now.UTC().Add(ttl),
		JTI:                 randomID("og_"),
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", nil, fmt.Errorf("marshalling observer delegation claims: %w", err)
	}
	signature := ed25519.Sign(privateKey, payload)
	token := strings.Join([]string{
		ObserverTokenPrefix,
		base64.RawURLEncoding.EncodeToString(payload),
		base64.RawURLEncoding.EncodeToString(signature),
	}, ".")
	return token, claims, nil
}

// VerifyObserverDelegation verifies a signed observer delegation.
func VerifyObserverDelegation(token string, bundle *VerifierBundle, now time.Time) (*ObserverDelegationClaims, error) {
	if bundle == nil {
		return nil, fmt.Errorf("verifier bundle is nil")
	}

	claims, payload, signature, err := parseObserverDelegation(token)
	if err != nil {
		return nil, err
	}
	if claims.Kind != ObserverDelegationKind {
		return nil, fmt.Errorf("unexpected observer delegation kind %q", claims.Kind)
	}
	if claims.NetworkID != ResolveNetworkID(bundle.NetworkID) {
		return nil, fmt.Errorf("observer delegation network_id = %q, want %q", claims.NetworkID, ResolveNetworkID(bundle.NetworkID))
	}
	if now.UTC().After(bundle.ExpiresAt.Add(maxClockSkew)) {
		return nil, fmt.Errorf("verifier bundle expired")
	}
	if now.UTC().Before(claims.IssuedAt.Add(-maxClockSkew)) {
		return nil, fmt.Errorf("observer delegation issued in the future")
	}
	if now.UTC().After(claims.ExpiresAt.Add(maxClockSkew)) {
		return nil, fmt.Errorf("observer delegation expired")
	}

	var publicKey ed25519.PublicKey
	for _, key := range bundle.Keys {
		if key.KeyID != claims.KeyID {
			continue
		}
		if key.Algorithm != AlgorithmEd25519 {
			return nil, fmt.Errorf("unsupported verifier algorithm %q", key.Algorithm)
		}
		keyBytes, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("decoding verifier public key: %w", err)
		}
		publicKey = ed25519.PublicKey(keyBytes)
		break
	}
	if len(publicKey) == 0 {
		return nil, fmt.Errorf("no verifier key for kid %q", claims.KeyID)
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return nil, fmt.Errorf("invalid observer delegation signature")
	}
	return claims, nil
}

// ParseObserverDelegation decodes observer delegation claims without verifying the signature.
func ParseObserverDelegation(token string) (*ObserverDelegationClaims, error) {
	claims, _, _, err := parseObserverDelegation(token)
	return claims, err
}

func parseObserverDelegation(token string) (*ObserverDelegationClaims, []byte, []byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != ObserverTokenPrefix {
		return nil, nil, nil, fmt.Errorf("invalid observer delegation format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decoding observer delegation payload: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decoding observer delegation signature: %w", err)
	}

	var claims ObserverDelegationClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, nil, nil, fmt.Errorf("parsing observer delegation payload: %w", err)
	}
	return &claims, payload, signature, nil
}

func randomID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b[:])
}
