// Package prefilter implements the local, zero-network pre-filter that routes
// obviously mechanical prompts to Tier 1 without calling the classifier.
package prefilter

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/pkg/schema"
)

// InspectBytes is the number of leading bytes (after trimming leading
// whitespace) that Match inspects. Mechanical intent is signaled at the start.
const InspectBytes = 512

// Rule is a named, start-anchored pattern that maps a prompt to a tier.
// Patterns are compiled case-insensitively. An empty Tier means Tier 1.
type Rule struct {
	Name    string
	Pattern string
	Tier    schema.Tier
}

// DefaultRules returns the built-in Tier 1 rules from the spec.
func DefaultRules() []Rule {
	return []Rule{
		{Name: "slash-mechanical", Pattern: `^/(cleanup|grammar|format|calibrate)\b`, Tier: schema.Tier1},
		{Name: "fix-typo", Pattern: `^fix typo:`, Tier: schema.Tier1},
		{Name: "format-json", Pattern: `^format json:`, Tier: schema.Tier1},
		{Name: "list-files", Pattern: `^list files:`, Tier: schema.Tier1},
		{Name: "check-spelling", Pattern: `^check spelling:`, Tier: schema.Tier1},
	}
}

type compiled struct {
	name  string
	re    *regexp.Regexp
	tier  schema.Tier
	model string
}

// Engine is a compiled, immutable set of rules. It is safe for concurrent use.
type Engine struct {
	rules []compiled
}

var _ schema.Prefilter = (*Engine)(nil)

// Compile builds an Engine from rules, compiling every regex once. When no
// rules are given, DefaultRules are used; otherwise exactly the given rules
// are used (append to DefaultRules() to extend the defaults).
func Compile(cfg config.Config, rules ...Rule) (*Engine, error) {
	if len(rules) == 0 {
		rules = DefaultRules()
	}
	e := &Engine{rules: make([]compiled, 0, len(rules))}
	for _, r := range rules {
		tier := r.Tier
		if tier == "" {
			tier = schema.Tier1
		}
		if !tier.Valid() {
			return nil, fmt.Errorf("prefilter: rule %q: %w: unknown tier %q", r.Name, schema.ErrInvalidDecision, tier)
		}
		re, err := regexp.Compile("(?i)" + r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("prefilter: rule %q: %w", r.Name, err)
		}
		e.rules = append(e.rules, compiled{name: r.Name, re: re, tier: tier, model: cfg.ModelFor(tier)})
	}
	return e, nil
}

// New is like Compile but panics on an invalid rule. Rules are programmer
// supplied constants, so an invalid pattern is a programming error (as with
// regexp.MustCompile).
func New(cfg config.Config, rules ...Rule) *Engine {
	e, err := Compile(cfg, rules...)
	if err != nil {
		panic(err)
	}
	return e
}

// Match reports whether a rule matches the start of prompt. Only the first
// InspectBytes bytes after leading whitespace are inspected. On a hit it
// returns a validated-shape Tier decision with Source=SourcePrefilter.
func (e *Engine) Match(prompt string) (schema.RoutingDecision, bool) {
	start := time.Now()
	head := strings.TrimLeftFunc(prompt, unicode.IsSpace)
	if len(head) > InspectBytes {
		head = head[:InspectBytes]
	}
	for i := range e.rules {
		r := &e.rules[i]
		if !r.re.MatchString(head) {
			continue
		}
		return schema.RoutingDecision{
			TaskID:        schema.NewID(),
			TaskTier:      r.tier,
			SelectedModel: r.model,
			EffortLevel:   schema.EffortLow,
			Rationale:     "prefilter:" + r.name,
			Source:        schema.SourcePrefilter,
			LatencyMicros: time.Since(start).Microseconds(),
		}, true
	}
	return schema.RoutingDecision{}, false
}
