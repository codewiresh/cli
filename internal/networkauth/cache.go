package networkauth

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// FetchBundleFunc loads the latest verifier bundle.
type FetchBundleFunc func(context.Context) (*VerifierBundle, error)

// BundleCache caches verifier bundles with expiry-based refresh.
type BundleCache struct {
	fetch FetchBundleFunc

	mu     sync.Mutex
	bundle *VerifierBundle
}

// NewBundleCache returns a cache backed by fetch.
func NewBundleCache(fetch FetchBundleFunc) *BundleCache {
	return &BundleCache{fetch: fetch}
}

// Get returns a cached verifier bundle or refreshes it when stale.
func (c *BundleCache) Get(ctx context.Context) (*VerifierBundle, error) {
	if c == nil || c.fetch == nil {
		return nil, fmt.Errorf("bundle cache fetcher is nil")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.bundle != nil && time.Now().UTC().Before(c.bundle.ExpiresAt.Add(-30*time.Second)) {
		return c.bundle, nil
	}

	bundle, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}
	c.bundle = bundle
	return bundle, nil
}
