package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/swiftj/jevons/internal/config"
	"github.com/swiftj/jevons/pkg/schema"
)

// ErrLowConfidence means Jev answered, but its tier confidence was below
// cfg.JevMinConfidence. The decision returned alongside it is still valid.
var ErrLowConfidence = errors.New("classifier: jev confidence below threshold")

// Question ids sent to Jev. The model never sees them.
const (
	jevTierQuestion   = "task_tier"
	jevEffortQuestion = "effort"
)

// jevTierCriteria mirrors the tier rubric in SystemPrompt.
var jevTierCriteria = map[schema.Tier]string{
	schema.Tier1: "Mechanical edits, formatting, lookups, short factual answers",
	schema.Tier2: "Standard coding, debugging, multi-file edits, summaries",
	schema.Tier3: "Architecture, novel design, long multi-step reasoning, high-stakes review",
}

// jevEffortLevels are the Score levels, lowest first; the index maps to effort.
var jevEffortLevels = []string{
	"Little reasoning needed: the answer is direct",
	"Some reasoning needed: a few steps or checks",
	"Deep reasoning needed: many steps, trade-offs, or careful verification",
}

var jevEfforts = []schema.Effort{schema.EffortLow, schema.EffortMedium, schema.EffortHigh}

// Jev classifies prompts with TypeSafe's Jev System One model via
// POST /v1/systemone. It is safe for concurrent use.
type Jev struct {
	cfg     config.Config
	http    *http.Client
	backoff time.Duration
}

var _ schema.Classifier = (*Jev)(nil)

// NewJev returns a Jev classifier. A nil httpClient is replaced by one whose
// Timeout is cfg.ClassifierTimeout.
func NewJev(cfg config.Config, httpClient *http.Client) *Jev {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.ClassifierTimeout}
	}
	return &Jev{cfg: cfg, http: httpClient, backoff: defaultBackoff}
}

type jevQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria"`
}

type jevRequest struct {
	Model     string                 `json:"model"`
	State     map[string]string      `json:"state"`
	Questions map[string]jevQuestion `json:"questions"`
}

type jevAnswer struct {
	Type       string   `json:"type"`
	Choice     string   `json:"choice"`
	Score      *float64 `json:"score"`
	Confidence float64  `json:"confidence"`
}

type jevResponse struct {
	Model   string               `json:"model"`
	Answers map[string]jevAnswer `json:"answers"`
}

// Classify asks Jev for a tier (Choice) and an effort level (Score) in one
// request. Transport failures and non-200 responses wrap
// schema.ErrClassifierUnavailable; malformed answers wrap
// schema.ErrInvalidDecision. When the tier confidence is below
// cfg.JevMinConfidence the decision is returned together with an error
// wrapping ErrLowConfidence.
func (j *Jev) Classify(ctx context.Context, req schema.RouteRequest) (schema.RoutingDecision, error) {
	start := time.Now()
	key := ""
	if j.cfg.TypeSafeAPIKeyEnv != "" {
		key = os.Getenv(j.cfg.TypeSafeAPIKeyEnv)
	}
	if key == "" {
		return schema.RoutingDecision{}, fmt.Errorf("jev: %w: api key env %q is empty", schema.ErrClassifierUnavailable, j.cfg.TypeSafeAPIKeyEnv)
	}

	tiers := make(map[string]string, len(jevTierCriteria))
	for t, desc := range jevTierCriteria {
		tiers[string(t)] = desc
	}
	body, err := json.Marshal(jevRequest{
		Model: j.cfg.TypeSafeModel,
		State: map[string]string{"user_prompt": truncateUTF8(req.Prompt, MaxPromptBytes)},
		Questions: map[string]jevQuestion{
			jevTierQuestion: {
				Type:         "choice",
				Instructions: "Which model tier should handle `user_prompt`, a request sent to an AI coding agent?",
				Criteria:     tiers,
			},
			jevEffortQuestion: {
				Type:         "score",
				Instructions: "How much reasoning does `user_prompt` need from the model that handles it?",
				Criteria:     jevEffortLevels,
			},
		},
	})
	if err != nil {
		return schema.RoutingDecision{}, fmt.Errorf("jev: marshal: %w", err)
	}

	resp, err := j.call(ctx, key, body)
	if err != nil {
		return schema.RoutingDecision{}, err
	}
	tierAns, ok := resp.Answers[jevTierQuestion]
	if !ok || tierAns.Type != "choice" {
		return schema.RoutingDecision{}, fmt.Errorf("jev: %w: missing tier answer", schema.ErrInvalidDecision)
	}
	effortAns, ok := resp.Answers[jevEffortQuestion]
	if !ok || effortAns.Type != "score" || effortAns.Score == nil {
		return schema.RoutingDecision{}, fmt.Errorf("jev: %w: missing effort answer", schema.ErrInvalidDecision)
	}

	tier := schema.Tier(tierAns.Choice)
	model := resp.Model
	if model == "" {
		model = j.cfg.TypeSafeModel
	}
	id := req.TaskID
	if id == "" {
		id = schema.NewID()
	}
	d := schema.RoutingDecision{
		TaskID:        id,
		TaskTier:      tier,
		SelectedModel: j.cfg.ModelFor(tier),
		EffortLevel:   effortFromScore(*effortAns.Score),
		Rationale:     fmt.Sprintf("%s tier confidence %.2f", model, tierAns.Confidence),
		Source:        schema.SourceJev,
		SessionID:     req.SessionID,
		LatencyMicros: time.Since(start).Microseconds(),
	}
	if err := d.Validate(); err != nil {
		return schema.RoutingDecision{}, fmt.Errorf("jev: %w", err)
	}
	if tierAns.Confidence < j.cfg.JevMinConfidence {
		return d, fmt.Errorf("jev: %w: %.2f < %.2f", ErrLowConfidence, tierAns.Confidence, j.cfg.JevMinConfidence)
	}
	return d, nil
}

// effortFromScore maps a 0..2 Score onto low/medium/high, rounding to the
// nearest level and clamping out-of-range values.
func effortFromScore(s float64) schema.Effort {
	i := int(math.Round(s))
	i = max(0, min(i, len(jevEfforts)-1))
	return jevEfforts[i]
}

// call performs the request with at most one retry for 429/5xx.
func (j *Jev) call(ctx context.Context, key string, body []byte) (jevResponse, error) {
	resp, retry, err := j.do(ctx, key, body)
	if err == nil || !retry {
		return resp, err
	}
	if werr := backoffWait(ctx, j.backoff); werr != nil {
		return jevResponse{}, fmt.Errorf("jev: backoff: %w: %w", schema.ErrClassifierUnavailable, werr)
	}
	resp, _, err = j.do(ctx, key, body)
	return resp, err
}

// do sends one request. retry reports whether the failure is retryable.
func (j *Jev) do(ctx context.Context, key string, body []byte) (out jevResponse, retry bool, err error) {
	url := strings.TrimRight(j.cfg.TypeSafeBaseURL, "/") + "/v1/systemone"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return out, false, fmt.Errorf("jev: build request: %w: %w", schema.ErrClassifierUnavailable, err)
	}
	hreq.Header.Set("Authorization", "Bearer "+key)
	hreq.Header.Set("Content-Type", "application/json")

	resp, err := j.http.Do(hreq)
	if err != nil {
		return out, false, fmt.Errorf("jev: request: %w: %w", schema.ErrClassifierUnavailable, err)
	}
	defer func() {
		// Drain so the connection can be reused; errors here are irrelevant.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close() // close error carries no actionable information
	}()

	if resp.StatusCode != http.StatusOK {
		// The error body is not surfaced: a validation error could echo request content.
		retry = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return out, retry, fmt.Errorf("jev: %w: http status %d", schema.ErrClassifierUnavailable, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return jevResponse{}, false, fmt.Errorf("jev: decode response: %w: %w", schema.ErrInvalidDecision, err)
	}
	return out, false, nil
}
