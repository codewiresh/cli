package networkauth

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ReplayCache tracks consumed credential JTIs until their expiry.
type ReplayCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewReplayCache returns a ready-to-use replay cache.
func NewReplayCache() *ReplayCache {
	return &ReplayCache{
		seen: make(map[string]time.Time),
	}
}

// ConsumeRuntime marks a runtime credential as used until expiry.
func (c *ReplayCache) ConsumeRuntime(claims *RuntimeClaims, now time.Time) error {
	if claims == nil {
		return fmt.Errorf("runtime claims are nil")
	}
	return c.consume("runtime", claims.NetworkID, claims.JTI, claims.ExpiresAt, now)
}

// ConsumeSender marks a sender delegation as used until expiry.
func (c *ReplayCache) ConsumeSender(claims *SenderDelegationClaims, now time.Time) error {
	if claims == nil {
		return fmt.Errorf("sender delegation claims are nil")
	}
	return c.consume("sender", claims.NetworkID, claims.JTI, claims.ExpiresAt, now)
}

// ConsumeObserver marks an observer delegation as used until expiry.
func (c *ReplayCache) ConsumeObserver(claims *ObserverDelegationClaims, now time.Time) error {
	if claims == nil {
		return fmt.Errorf("observer delegation claims are nil")
	}
	return c.consume("observer", claims.NetworkID, claims.JTI, claims.ExpiresAt, now)
}

func (c *ReplayCache) consume(kind, networkID, jti string, expiresAt, now time.Time) error {
	if c == nil {
		return nil
	}
	kind = strings.TrimSpace(kind)
	networkID = ResolveNetworkID(networkID)
	jti = strings.TrimSpace(jti)
	if kind == "" || jti == "" {
		return fmt.Errorf("credential replay key is incomplete")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for key, exp := range c.seen {
		if now.UTC().After(exp) {
			delete(c.seen, key)
		}
	}

	key := kind + "\x00" + networkID + "\x00" + jti
	if exp, exists := c.seen[key]; exists && now.UTC().Before(exp) {
		return fmt.Errorf("%s credential replay detected", kind)
	}

	c.seen[key] = expiresAt.UTC().Add(maxClockSkew)
	return nil
}
