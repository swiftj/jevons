# Daemon API

`jev-router` serves JSON over HTTP on `127.0.0.1:8942`, on the loopback interface only. Request
bodies are JSON objects of at most 256 KiB. Errors come back as `{"error": "..."}` with a
suitable status code.

| Method & path | Body | Returns |
|---------------|------|---------|
| `GET /healthz` | | `{"ok":true}` |
| `GET /status` | | Telemetry snapshot (below) |
| `POST /route` | `{"prompt", "task_id"?, "session_id"?, "essential"?}` | Routing decision |
| `POST /escalate` | `{"decision": <decision>, "cause": "..."}` | Tier 2 decision |
| `POST /session` | `{"session_id", "model", "cached_tokens", "last_activity"?}` | `{"ok":true}` |
| `POST /usage` | `{"tokens": n}` (1 to 1,000,000,000) | `{"ok":true}` |

## Routing decision

```json
{
  "task_id": "42373ac93e1371dd",
  "task_tier": "TIER_1",
  "selected_model": "claude-haiku-5-5",
  "effort_level": "low",
  "rationale": "prefilter:slash-mechanical",
  "source": "prefilter",
  "session_id": "chat-1",
  "latency_us": 17
}
```

`source` is one of `prefilter`, `cache_warm`, `classifier`, `throttle`, `escalation`.
`essential: true` on `/route` excludes the request from throttling.

## Status

```json
{
  "uptime": "3h12m4s",
  "routed_total": 214,
  "by_source": {"prefilter": 31, "classifier": 180, "cache_warm": 3},
  "by_tier": {"TIER_1": 77, "TIER_2": 129, "TIER_3": 8},
  "usage": [{"name": "5h", "used": 812000, "capacity": 0}, {"name": "7d", "used": 4100000, "capacity": 0}],
  "usage_errors": 0,
  "classifier_errors": 2,
  "last_classifier_error": "classifier: ... timeout",
  "warm_sessions": [],
  "throttled": false
}
```

## Errors

| Status | When |
|--------|------|
| 400 | Malformed JSON, empty prompt, invalid session or decision, tokens out of range |
| 403 | Cross-origin request from a disallowed `Origin` (only `null`, `file://` and loopback are allowed) |
| 405 | Wrong method (the response has an `Allow` header) |
| 413 | Body over 256 KiB |
| 421 | Non-loopback `Host` header |
| 501 | `/session` or `/usage` feeds not configured |
| 502 | Classifier unavailable (only where the router does not fall back) |
