package schema

import (
	"errors"
	"testing"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	ok := RoutingDecision{TaskID: "a", TaskTier: Tier1, SelectedModel: "m", EffortLevel: EffortLow}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid decision: %v", err)
	}
	cases := map[string]func(*RoutingDecision){
		"no id":      func(d *RoutingDecision) { d.TaskID = "" },
		"bad tier":   func(d *RoutingDecision) { d.TaskTier = "TIER_9" },
		"no model":   func(d *RoutingDecision) { d.SelectedModel = "" },
		"bad effort": func(d *RoutingDecision) { d.EffortLevel = "max" },
	}
	for name, mutate := range cases {
		d := ok
		mutate(&d)
		if err := d.Validate(); !errors.Is(err, ErrInvalidDecision) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestRatio(t *testing.T) {
	t.Parallel()
	if r := (UsageWindow{Used: 5, Capacity: 0}).Ratio(); r != 0 {
		t.Errorf("zero capacity ratio = %v", r)
	}
	if r := (UsageWindow{Used: 8, Capacity: 10}).Ratio(); r != 0.8 {
		t.Errorf("ratio = %v", r)
	}
}

func TestNewID(t *testing.T) {
	t.Parallel()
	a, b := NewID(), NewID()
	if len(a) != 16 || a == b {
		t.Errorf("NewID = %q, %q", a, b)
	}
	if (SystemClock{}).Now().IsZero() {
		t.Error("SystemClock zero")
	}
}
