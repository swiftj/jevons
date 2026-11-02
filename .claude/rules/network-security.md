---
paths:
  - "internal/proxy/**"
  - "internal/classifier/**"
  - "internal/client/**"
  - "internal/config/**"
---
# Network & Secrets Rules

- The telemetry server binds loopback only (`127.0.0.1:8942` default). Refuse non-loopback
  listen addresses and non-loopback `Host` headers. No wildcard CORS.
- Every `http.Server` sets ReadHeader/Read/Write/Idle timeouts; every `http.Client` has a timeout;
  every outbound request is built with `http.NewRequestWithContext`.
- Cap request bodies with `http.MaxBytesReader`; always close response bodies.
- API keys are read from the env var named by `cfg.APIKeyEnv` at call time. Never store keys in
  config files, structs that get serialized, logs, error messages, or test fixtures.
- Never log prompt contents or model output — log lengths, IDs, latency, and status only.
- Model IDs come from `cfg.Models`; never hard-code a model string outside `internal/config` defaults.
