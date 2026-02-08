# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

- `go build ./cmd/proxy` — build the proxy binary
- `go build ./cmd/mockserver` — build the mock upstream server
- `QLITE_CONFIG=config/config.yaml go run ./cmd/proxy` — run the proxy
- Mock setup: `go run ./cmd/mockserver -port 9999 -latency 50ms` + `QLITE_CONFIG=config/config.mock.yaml go run ./cmd/proxy`

## Testing

- `go test ./...` — run all tests
- `go test ./internal/server -run TestName -v` — run a single test
- `go test ./internal/server -bench . -benchmem` — run benchmarks
- P99 overhead benchmark: `TestProxyOverhead_P99` asserts <10ms proxy overhead

## Load Testing

- Requires 3 terminals: mockserver, proxy, locust
- `locust -f loadtest/locustfile.py --host http://localhost:8080 --users 20 --spawn-rate 5 --run-time 60s --headless`
- Real API integration test: `OPENAI_API_KEY=... go run loadtest/realtest.go` or `ANTHROPIC_API_KEY=... QLITE_TEST_PROVIDER=anthropic go run loadtest/realtest.go`

## Architecture

- Go reverse proxy for OpenAI-compatible LLM APIs, stdlib `net/http` only
- Pipeline pattern: `Stage` (non-streaming) + `StreamStage` (streaming) interfaces in `internal/pipeline`
- Provider abstraction: `Provider` interface in `internal/provider`, `Registry` maps model names to providers
- Multi-provider: OpenAI, Anthropic, Google — clients always send OpenAI format, proxy translates to native API
- Exact cache (`internal/cache`): SHA-256 of (model, messages, temperature, top_p); stream flag excluded from key so streaming/non-streaming share entries
- Cache pipeline stage (`internal/pipeline/cache.go`) is first in chain; stores on MISS, replays SSE on streaming HIT
- Response headers: `X-Cache` (HIT/MISS), `X-Request-Cost`, `X-Tokens-Saved`, `X-Provider`
- SSE Writer interface lives in `internal/sse` as a **leaf package** to break import cycle (server → pipeline → provider → sse)
- Middleware: standard `func(http.Handler) http.Handler` chain; `statusWriter.Unwrap()` enables `http.ResponseController` through middleware
- Config: YAML with `os.ExpandEnv()` for `${ENV_VAR}` substitution

## Key Conventions

- No frameworks — stdlib `net/http` only, for low latency
- Buffer pooling via `sync.Pool` for request body serialization (provider)
- Tiktoken encoding cached with `sync.RWMutex` double-check pattern (tokenizer)
- Tests use `httptest.NewServer` for mock OpenAI servers
- `testSSEWriter` implements `sse.Writer` for capturing streaming events in tests
- New shared interfaces consumed by both provider and server must go in a leaf package (like `internal/sse`) to avoid import cycles
- Anthropic native API: `x-api-key` header, `anthropic-version: 2023-06-01`, POST `/v1/messages`, `max_tokens` required (default 4096)
- Standalone Go scripts in `loadtest/` use `package main` and `go run` — no test framework dependencies
