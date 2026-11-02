// Package classifier implements schema.Classifier by asking a small Anthropic
// model, via the raw Messages API, to return a micro-JSON routing decision.
package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/swiftj/jevons/internal/config"
	"github.com/swiftj/jevons/pkg/schema"
)

const (
	// AnthropicVersion is the anthropic-version header value.
	AnthropicVersion = "2023-06-01"
	// MaxTokens is the max_tokens sent with each classification request.
	MaxTokens = 200
	// MaxPromptBytes is the cap on user prompt bytes sent to the classifier.
	MaxPromptBytes = 8 * 1024
	// maxResponseBytes caps how much of a response body is read.
	maxResponseBytes = 1 << 20
	// defaultBackoff is the base retry delay; jitter of +/-50% is applied.
	defaultBackoff = 200 * time.Millisecond
)

// Client classifies prompts through the Anthropic Messages API.
// It is safe for concurrent use.
type Client struct {
	cfg     config.Config
	http    *http.Client
	backoff time.Duration
}

var _ schema.Classifier = (*Client)(nil)

// New returns a Client. A nil httpClient is replaced by one whose Timeout is
// cfg.ClassifierTimeout.
func New(cfg config.Config, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.ClassifierTimeout}
	}
	return &Client{cfg: cfg, http: httpClient, backoff: defaultBackoff}
}

type cacheControl struct {
	Type string `json:"type"`
}

type systemBlock struct {
	Type         string       `json:"type"`
	Text         string       `json:"text"`
	CacheControl cacheControl `json:"cache_control"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messagesRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    []systemBlock `json:"system"`
	Messages  []message     `json:"messages"`
}

type messagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type microDecision struct {
	TaskTier    schema.Tier   `json:"task_tier"`
	EffortLevel schema.Effort `json:"effort_level"`
	Rationale   string        `json:"rationale"`
}

// Classify asks the classifier model for a tier decision. Transport failures,
// timeouts, and non-200 responses wrap schema.ErrClassifierUnavailable;
// unparseable or invalid model output wraps schema.ErrInvalidDecision.
func (c *Client) Classify(ctx context.Context, req schema.RouteRequest) (schema.RoutingDecision, error) {
	start := time.Now()
	key := ""
	if c.cfg.APIKeyEnv != "" {
		key = os.Getenv(c.cfg.APIKeyEnv)
	}
	if key == "" {
		return schema.RoutingDecision{}, fmt.Errorf("classifier: %w: api key env %q is empty", schema.ErrClassifierUnavailable, c.cfg.APIKeyEnv)
	}

	body, err := json.Marshal(messagesRequest{
		Model:     c.cfg.ClassifierModel,
		MaxTokens: MaxTokens,
		System: []systemBlock{{
			Type:         "text",
			Text:         SystemPrompt,
			CacheControl: cacheControl{Type: "ephemeral"},
		}},
		Messages: []message{{Role: "user", Content: truncateUTF8(req.Prompt, MaxPromptBytes)}},
	})
	if err != nil {
		return schema.RoutingDecision{}, fmt.Errorf("classifier: marshal: %w", err)
	}

	text, err := c.call(ctx, key, body)
	if err != nil {
		return schema.RoutingDecision{}, err
	}
	md, err := parseDecision(text)
	if err != nil {
		return schema.RoutingDecision{}, err
	}

	id := req.TaskID
	if id == "" {
		id = schema.NewID()
	}
	d := schema.RoutingDecision{
		TaskID:        id,
		TaskTier:      md.TaskTier,
		SelectedModel: c.cfg.ModelFor(md.TaskTier),
		EffortLevel:   md.EffortLevel,
		Rationale:     md.Rationale,
		Source:        schema.SourceClassifier,
		SessionID:     req.SessionID,
		LatencyMicros: time.Since(start).Microseconds(),
	}
	if err := d.Validate(); err != nil {
		return schema.RoutingDecision{}, fmt.Errorf("classifier: %w", err)
	}
	return d, nil
}

// call performs the request with at most one retry for 429/529/5xx and
// returns the first text content block.
func (c *Client) call(ctx context.Context, key string, body []byte) (string, error) {
	text, retry, err := c.do(ctx, key, body)
	if err == nil || !retry {
		return text, err
	}
	if werr := c.wait(ctx); werr != nil {
		return "", fmt.Errorf("classifier: backoff: %w: %w", schema.ErrClassifierUnavailable, werr)
	}
	text, _, err = c.do(ctx, key, body)
	return text, err
}

func (c *Client) wait(ctx context.Context) error {
	d := c.backoff
	if d > 0 {
		// #nosec G404 -- retry jitter, not security sensitive.
		d = d/2 + time.Duration(rand.Int64N(int64(d)))
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do sends one request. retry reports whether the failure is retryable.
func (c *Client) do(ctx context.Context, key string, body []byte) (text string, retry bool, err error) {
	url := strings.TrimRight(c.cfg.AnthropicBaseURL, "/") + "/v1/messages"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", false, fmt.Errorf("classifier: build request: %w: %w", schema.ErrClassifierUnavailable, err)
	}
	hreq.Header.Set("x-api-key", key)
	hreq.Header.Set("anthropic-version", AnthropicVersion)
	hreq.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(hreq)
	if err != nil {
		return "", false, fmt.Errorf("classifier: request: %w: %w", schema.ErrClassifierUnavailable, err)
	}
	defer func() {
		// Drain so the connection can be reused; errors here are irrelevant.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close() // close error carries no actionable information
	}()

	if resp.StatusCode != http.StatusOK {
		retry = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return "", retry, fmt.Errorf("classifier: %w: http status %d%s", schema.ErrClassifierUnavailable, resp.StatusCode, apiErrorType(resp.Body))
	}

	var mr messagesResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&mr); err != nil {
		return "", false, fmt.Errorf("classifier: decode response: %w: %w", schema.ErrInvalidDecision, err)
	}
	for _, blk := range mr.Content {
		if blk.Type == "text" {
			return blk.Text, false, nil
		}
	}
	return "", false, fmt.Errorf("classifier: %w: no text content block", schema.ErrInvalidDecision)
}

// parseDecision accepts a bare JSON object, optionally surrounded by
// whitespace and wrapped in a single ```json (or ```) fence.
func parseDecision(text string) (microDecision, error) {
	s := strings.TrimSpace(text)
	if rest, ok := strings.CutPrefix(s, "```"); ok {
		inner, ok := strings.CutSuffix(rest, "```")
		if !ok {
			return microDecision{}, fmt.Errorf("classifier: %w: unterminated code fence", schema.ErrInvalidDecision)
		}
		inner = strings.TrimPrefix(inner, "json")
		s = strings.TrimSpace(inner)
	}
	if !strings.HasPrefix(s, "{") {
		return microDecision{}, fmt.Errorf("classifier: %w: response is not a JSON object", schema.ErrInvalidDecision)
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var md microDecision
	if err := dec.Decode(&md); err != nil {
		return microDecision{}, fmt.Errorf("classifier: %w: %w", schema.ErrInvalidDecision, err)
	}
	if strings.TrimSpace(s[dec.InputOffset():]) != "" {
		return microDecision{}, fmt.Errorf("classifier: %w: trailing data after JSON object", schema.ErrInvalidDecision)
	}
	return md, nil
}

// truncateUTF8 returns at most n bytes of s without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// apiErrorType returns " (<type>)" from an Anthropic error body such as
// {"error":{"type":"invalid_request_error"}}, or "" when absent. Only the type
// is surfaced; the message is dropped since it could echo request content.
func apiErrorType(body io.Reader) string {
	var e struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxResponseBytes)).Decode(&e); err != nil || e.Error.Type == "" {
		return ""
	}
	return " (" + e.Error.Type + ")"
}
