package classifier

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/swiftj/jevons/pkg/schema"
)

// Fallback reasons prefixed to a fallback decision's rationale.
const (
	ReasonPrimaryFailed        = "jev_unavailable"
	ReasonPrimaryLowConfidence = "jev_low_confidence"
)

// Chain asks a primary classifier (Jev) first and a fallback (the LLM
// classifier) when the primary fails or reports ErrLowConfidence.
type Chain struct {
	primary, fallback schema.Classifier
	logger            *slog.Logger
}

var _ schema.Classifier = (*Chain)(nil)

// NewChain returns a Chain. A nil logger discards output.
func NewChain(primary, fallback schema.Classifier, logger *slog.Logger) *Chain {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Chain{primary: primary, fallback: fallback, logger: logger}
}

// Classify returns the primary decision when it succeeds. Otherwise it returns
// the fallback decision with its rationale prefixed by the reason. When the
// fallback also fails, a low-confidence primary decision is still returned;
// any other double failure returns both errors.
func (c *Chain) Classify(ctx context.Context, req schema.RouteRequest) (schema.RoutingDecision, error) {
	d, err := c.primary.Classify(ctx, req)
	if err == nil {
		return d, nil
	}
	lowConf := errors.Is(err, ErrLowConfidence)
	reason := ReasonPrimaryFailed
	if lowConf {
		reason = ReasonPrimaryLowConfidence
		c.logger.InfoContext(ctx, "jev unsure; asking fallback classifier", "task_id", req.TaskID, "err", err.Error())
	} else {
		c.logger.WarnContext(ctx, "jev classifier failed; asking fallback classifier", "task_id", req.TaskID, "err", err.Error())
	}

	fd, ferr := c.fallback.Classify(ctx, req)
	if ferr == nil {
		fd.Rationale = reason + ": " + fd.Rationale
		return fd, nil
	}
	if lowConf {
		c.logger.WarnContext(ctx, "fallback classifier failed; keeping low-confidence jev decision", "task_id", req.TaskID, "err", ferr.Error())
		return d, nil
	}
	return schema.RoutingDecision{}, fmt.Errorf("classifier: jev: %w; fallback: %w", err, ferr)
}
