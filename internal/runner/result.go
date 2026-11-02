package runner

import (
	"encoding/json"
	"strings"

	"github.com/swiftj/jevons/pkg/schema"
)

// claudeResult is the subset of `claude -p --output-format json` output the
// runner reads.
type claudeResult struct {
	Type      string `json:"type"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	Usage     struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// applyResult parses res.Stdout as a claude JSON result. On success it replaces
// Stdout with the result text and fills Tokens and SessionID; Tokens counts all
// input, cache, and output tokens. Output that is not a JSON result object is
// left untouched. It reports whether the output parsed and whether claude
// flagged the run as an error.
func applyResult(res *schema.RunResult) (parsed, isError bool) {
	cr, ok := parseResult(res.Stdout)
	if !ok {
		return false, false
	}
	u := cr.Usage
	res.Stdout = cr.Result
	res.Tokens = u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	res.SessionID = cr.SessionID
	return true, cr.IsError
}

// parseResult decodes stdout as a claude result object. claude may print
// warnings to stdout before the JSON (e.g. "claude.ai connectors are disabled"
// when ANTHROPIC_API_KEY is set), so if the whole output does not parse, the
// last line is tried on its own.
func parseResult(stdout string) (claudeResult, bool) {
	s := strings.TrimSpace(stdout)
	var cr claudeResult
	if err := json.Unmarshal([]byte(s), &cr); err == nil && cr.Type == "result" {
		return cr, true
	}
	i := strings.LastIndexByte(s, '\n')
	if i < 0 {
		return claudeResult{}, false
	}
	cr = claudeResult{}
	if err := json.Unmarshal([]byte(s[i+1:]), &cr); err != nil || cr.Type != "result" {
		return claudeResult{}, false
	}
	return cr, true
}
