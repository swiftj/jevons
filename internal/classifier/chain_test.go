package classifier

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/swiftj/jevons/internal/mocks"
	"github.com/swiftj/jevons/pkg/schema"
)

// lowConfJev returns a valid decision together with ErrLowConfidence.
type lowConfJev struct{ d schema.RoutingDecision }

func (l lowConfJev) Classify(context.Context, schema.RouteRequest) (schema.RoutingDecision, error) {
	return l.d, fmt.Errorf("jev: %w", ErrLowConfidence)
}

func TestChain(t *testing.T) {
	t.Parallel()
	jevD := schema.RoutingDecision{TaskID: "t", TaskTier: schema.Tier1, SelectedModel: "h", EffortLevel: schema.EffortLow, Source: schema.SourceJev, Rationale: "jev"}
	llmD := schema.RoutingDecision{TaskID: "t", TaskTier: schema.Tier2, SelectedModel: "s", EffortLevel: schema.EffortMedium, Source: schema.SourceClassifier, Rationale: "llm"}
	down := fmt.Errorf("x: %w", schema.ErrClassifierUnavailable)

	cases := []struct {
		name          string
		primary       schema.Classifier
		fallback      *mocks.StubClassifier
		wantSource    schema.DecisionSource
		wantRationale string
		wantErr       bool
		fallbackCalls int
	}{
		{"jev ok", &mocks.StubClassifier{Decision: jevD}, &mocks.StubClassifier{Decision: llmD}, schema.SourceJev, "jev", false, 0},
		{"jev down", &mocks.StubClassifier{Err: down}, &mocks.StubClassifier{Decision: llmD}, schema.SourceClassifier, "jev_unavailable: llm", false, 1},
		{"jev unsure", lowConfJev{jevD}, &mocks.StubClassifier{Decision: llmD}, schema.SourceClassifier, "jev_low_confidence: llm", false, 1},
		{"jev unsure, llm down", lowConfJev{jevD}, &mocks.StubClassifier{Err: down}, schema.SourceJev, "jev", false, 1},
		{"both down", &mocks.StubClassifier{Err: down}, &mocks.StubClassifier{Err: down}, "", "", true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := NewChain(tc.primary, tc.fallback, nil).Classify(context.Background(), schema.RouteRequest{TaskID: "t"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tc.wantErr {
				if !errors.Is(err, schema.ErrClassifierUnavailable) {
					t.Errorf("err should wrap ErrClassifierUnavailable: %v", err)
				}
			} else if d.Source != tc.wantSource || d.Rationale != tc.wantRationale {
				t.Errorf("decision = %+v", d)
			}
			if n := tc.fallback.Calls(); n != tc.fallbackCalls {
				t.Errorf("fallback calls = %d, want %d", n, tc.fallbackCalls)
			}
		})
	}
}
