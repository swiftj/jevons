// Package client is a small HTTP client for the jev-router daemon's loopback API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/swiftj/jevons/pkg/schema"
)

// DefaultTimeout bounds each request when no http.Client is supplied.
const DefaultTimeout = 5 * time.Second

// maxResponseBytes caps how much of a response body is read.
const maxResponseBytes = 1 << 20

// ErrUnreachable wraps transport failures (daemon not running, connection refused).
var ErrUnreachable = errors.New("client: daemon unreachable")

// Client talks to a jev-router daemon.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for the daemon at addr (host:port). A nil hc uses a
// client with DefaultTimeout.
func New(addr string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{base: "http://" + addr, http: hc}
}

// Route asks the daemon to pick a model for req.
func (c *Client) Route(ctx context.Context, req schema.RouteRequest) (schema.RoutingDecision, error) {
	var d schema.RoutingDecision
	if err := c.post(ctx, "/route", req, &d); err != nil {
		return schema.RoutingDecision{}, err
	}
	return d, d.Validate()
}

// Escalate asks the daemon to promote a failed Tier 1 decision.
func (c *Client) Escalate(ctx context.Context, prev schema.RoutingDecision, cause string) (schema.RoutingDecision, error) {
	body := struct {
		Decision schema.RoutingDecision `json:"decision"`
		Cause    string                 `json:"cause"`
	}{prev, cause}
	var d schema.RoutingDecision
	if err := c.post(ctx, "/escalate", body, &d); err != nil {
		return schema.RoutingDecision{}, err
	}
	return d, d.Validate()
}

// ReportUsage records consumed tokens with the daemon.
func (c *Client) ReportUsage(ctx context.Context, tokens int64) error {
	return c.post(ctx, "/usage", map[string]int64{"tokens": tokens}, nil)
}

// post sends in as JSON and decodes a 200 response into out (when non-nil).
func (c *Client) post(ctx context.Context, path string, in, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("client: %s: encode: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("client: %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnreachable, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("client: %s: read: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		// Best effort: fall back to the bare status when the body is not JSON.
		_ = json.Unmarshal(body, &e)
		return fmt.Errorf("client: %s: status %d: %s", path, resp.StatusCode, e.Error)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("client: %s: decode: %w", path, err)
	}
	return nil
}
