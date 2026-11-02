# 02 — Pre-Filter & Classifier (Wave 2-A)

**Owns:** `internal/prefilter/**`, `internal/classifier/**`
**Consumes:** `pkg/schema`, `internal/config`, `internal/mocks` (read-only)

## A. Local Pre-Filter Engine — `internal/prefilter`

Implements `schema.Prefilter`. Zero network, zero allocation-heavy work in the hot path.

### Requirements
- `New(cfg config.Config, rules ...Rule) *Engine` compiles all regexes **once** at construction.
- `Match` must complete in **< 1ms** for prompts up to 64 KiB (enforced by benchmark).
- Only the **first 512 bytes** of the prompt (after trimming leading whitespace) are inspected;
  mechanical intent is always signaled at the start.
- Matching is case-insensitive.
- On hit: returns `TaskTier=Tier1`, `SelectedModel=cfg.Models[Tier1]`, `EffortLevel=low`,
  `Source=SourcePrefilter`, `Rationale="prefilter:<rule name>"`, fresh `TaskID`
  (`crypto/rand` hex, 16 chars), `LatencyMicros` measured.

### Default rules
| Rule name | Pattern (anchored at start) |
|-----------|-----------------------------|
| `slash-mechanical` | `^/(cleanup\|grammar\|format\|calibrate)\b` |
| `fix-typo` | `^fix typo:` |
| `format-json` | `^format json:` |
| `list-files` | `^list files:` |
| `check-spelling` | `^check spelling:` |

- `Rule{Name string; Pattern string; Tier schema.Tier}` — custom rules may target any tier,
  but defaults are Tier 1.
- Non-matching slash commands (e.g. `/deploy`) must **not** match.

### Tests (target 100% statement coverage, per original Phase 1)
- Table-driven: every default rule hit, case variants, leading whitespace, near-misses
  (`/cleanupx`, `fix typos`, mid-sentence `fix typo:`), empty prompt, 64 KiB prompt.
- `BenchmarkMatch` on hit and miss paths; a test asserts p99 of 10k iterations < 1ms.
- Concurrency: `t.Parallel` + `-race` with shared `*Engine`.

## B. Micro-JSON Classifier — `internal/classifier`

Implements `schema.Classifier` by calling the Anthropic Messages API directly with
`net/http` (no SDK — minimal dependencies).

### Request
- `POST {AnthropicBaseURL}/v1/messages`, headers `x-api-key` (from `os.Getenv(cfg.APIKeyEnv)`),
  `anthropic-version: 2023-06-01`, `content-type: application/json`.
- `model = cfg.ClassifierModel`, `max_tokens = 200`. Do **not** send `temperature`: current models reject it with a 400.
- Minimal system prompt (constant in `prompt.go`) instructing the model to reply with **only**
  a JSON object `{"task_tier","effort_level","rationale"}` using the tier rubric:
  - `TIER_1`: mechanical edits, formatting, lookups, short factual answers.
  - `TIER_2`: standard coding, debugging, multi-file edits, summaries.
  - `TIER_3`: architecture, novel design, long multi-step reasoning, high-stakes review.
- Use prompt caching on the system block (`cache_control: {"type":"ephemeral"}`).
- Truncate the user prompt to 8 KiB before sending.

### Response handling
- Extract the first text content block; tolerate surrounding whitespace and a single
  ```` ```json ```` fence; reject anything else with `ErrInvalidDecision`.
- Fill `TaskID` (from request or generated), `SelectedModel` from `cfg.Models`,
  `Source=SourceClassifier`, `LatencyMicros`; then `Validate()`.
- HTTP 429/5xx/timeout → wrap `ErrClassifierUnavailable`. One retry with 200ms jittered
  backoff for 429/529/5xx only; respect `ctx`.
- Missing API key → `ErrClassifierUnavailable` without making a request.

### Constructor
`New(cfg config.Config, httpClient *http.Client) *Client` — nil client → `&http.Client{Timeout: cfg.ClassifierTimeout}`.

### Tests
- `httptest.Server` fakes: valid JSON, fenced JSON, prose response, invalid tier, 429-then-200,
  500×2, slow server exceeding timeout, ctx cancellation, missing key.
- Assert request body shape (model, max_tokens, system cache_control) and headers.
- Assert the API key never appears in returned errors.
