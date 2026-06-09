// Package temporal provides a thin optional wrapper around the Temporal workflow SDK.
// If TEMPORAL_HOST is not configured, the client is nil and all workflow
// operations are silently skipped.
package temporal

import (
	"fmt"

	"go.temporal.io/sdk/client"
)

// Client wraps the Temporal SDK client.
// Use New() to create one; the result may be nil when Temporal is not configured.
type Client struct {
	client.Client
}

// New dials the Temporal server at hostPort and returns a wrapped client.
// Returns (nil, nil) when hostPort is empty — callers must handle nil.
func New(hostPort string) (*Client, error) {
	if hostPort == "" {
		return nil, nil
	}
	c, err := client.Dial(client.Options{HostPort: hostPort})
	if err != nil {
		return nil, fmt.Errorf("temporal: dial %s: %w", hostPort, err)
	}
	return &Client{Client: c}, nil
}
