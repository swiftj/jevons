# 05 — Claude Mods Telemetry Bridge (Wave 2-D)

**Owns:** `internal/proxy/**`
**Consumes:** `pkg/schema`, `internal/config`, `internal/mocks` (read-only)

A local-only HTTP server that exposes router status and on-demand classification to
desktop extension mods.

```go
func NewServer(cfg config.Config, r schema.Router, logger *slog.Logger) *Server
func (s *Server) Handler() http.Handler           // for httptest
func (s *Server) ListenAndServe(ctx context.Context) error // blocks; graceful shutdown on ctx.Done
```

## Binding & Hardening

- Listen on `cfg.ListenAddr`; **refuse to start** if the host is not a loopback address
  (`127.0.0.1`, `::1`, `localhost`).
- `http.Server` timeouts: ReadHeader 2s, Read 5s, Write 10s, Idle 60s.
- Request body limit 256 KiB (`http.MaxBytesReader`).
- Reject requests whose `Host` header is not loopback (DNS-rebinding defense) → 421.
- CORS: no wildcard. Allow `Origin` only if it is `null`, `file://`, or a loopback origin;
  respond to `OPTIONS` preflight accordingly.
- Graceful shutdown with 5s deadline.

## Endpoints

| Method & Path | Behavior |
|---------------|----------|
| `GET /status` | `200` + `schema.StatusSnapshot` JSON from `Router.Status` |
| `POST /route` | Body `schema.RouteRequest` → `Router.Route` → `200` + `RoutingDecision` |
| `POST /escalate` | Body `{"decision": RoutingDecision, "cause": string}` → `Router.Escalate` |
| `GET /healthz` | `200 {"ok":true}` — no router call |

- Wrong method → `405` with `Allow` header. Unknown path → `404`.
- Malformed JSON / empty prompt → `400 {"error": "..."}`.
- Router errors → `502` for classifier unavailability, else `500`; error text must not
  include prompt contents.
- All responses `Content-Type: application/json; charset=utf-8`.
- Per-request timeout via `context.WithTimeout(r.Context(), 5s)` passed to the router.
- Structured access log via `log/slog` (method, path, status, latency) — never log prompts.

Use Go 1.22 `http.ServeMux` method patterns (`"GET /status"`, `"POST /route"`).

## Tests

- `httptest` against `Handler()` with `StubRouter`: each endpoint happy path, 400/404/405,
  body > 256 KiB → 413, non-loopback Host → 421, CORS allowed vs. disallowed origin.
- `ListenAndServe` refuses `0.0.0.0:8942` and `:8942`.
- Start on `127.0.0.1:0`, cancel ctx, assert clean return within 6s.
- Assert a prompt string sent to `/route` never appears in captured slog output.
