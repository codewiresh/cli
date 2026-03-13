package platform

import (
	"fmt"
	"time"
)

// ListResources returns all resources the user has access to across all orgs.
func (c *Client) ListResources() ([]PlatformResource, error) {
	var resources []PlatformResource
	if err := c.do("GET", "/api/v1/resources", nil, &resources); err != nil {
		return nil, err
	}
	return resources, nil
}

// GetResource returns a single resource by ID or slug.
func (c *Client) GetResource(idOrSlug string) (*PlatformResource, error) {
	var resource PlatformResource
	if err := c.do("GET", "/api/v1/resources/"+idOrSlug, nil, &resource); err != nil {
		return nil, err
	}
	return &resource, nil
}

// CreateResource creates a new resource.
func (c *Client) CreateResource(req *CreateResourceRequest) (*CreateResourceResult, error) {
	var result CreateResourceResult
	if err := c.do("POST", "/api/v1/resources", req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// WaitForResource polls until the resource reaches the target status or the timeout expires.
func (c *Client) WaitForResource(resourceID string, targetStatus string, interval, timeout time.Duration) (*PlatformResource, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resource, err := c.GetResource(resourceID)
		if err != nil {
			return nil, fmt.Errorf("poll resource: %w", err)
		}
		if resource.Status == targetStatus {
			return resource, nil
		}
		if resource.Status == "failed" || resource.Status == "error" {
			msg := resource.ProvisionError
			if msg == "" {
				msg = resource.Status
			}
			return resource, fmt.Errorf("resource %s: %s", resource.Status, msg)
		}
		time.Sleep(interval)
	}
	return nil, fmt.Errorf("timed out waiting for resource to reach %q status", targetStatus)
}

// WaitForCheckout polls until the resource's billing_status is no longer "checkout_pending".
func (c *Client) WaitForCheckout(resourceID string, interval, timeout time.Duration) (*PlatformResource, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resource, err := c.GetResource(resourceID)
		if err != nil {
			return nil, fmt.Errorf("poll checkout: %w", err)
		}
		if resource.BillingStatus != "checkout_pending" {
			return resource, nil
		}
		time.Sleep(interval)
	}
	return nil, fmt.Errorf("timed out waiting for checkout completion")
}

// DeleteResource deletes a resource by ID or slug.
func (c *Client) DeleteResource(idOrSlug string) error {
	return c.do("DELETE", "/api/v1/resources/"+idOrSlug, nil, nil)
}

