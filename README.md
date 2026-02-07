# qlite

A lightweight reverse proxy for OpenAI-compatible LLM APIs, built with Go's standard library (`net/http`) for minimal latency overhead.

## Features

- OpenAI-compatible `/v1/chat/completions` endpoint (streaming and non-streaming)
- Provider abstraction with model-based routing
- Token counting via tiktoken
- Request ID tracking, structured JSON logging, CORS, panic recovery
- Pipeline architecture for extensible request/response processing
- Exact-match response cache with TTL and LRU eviction
- Optional pprof profiling endpoint

## Quick Start

```bash
# Set your API key
export OPENAI_API_KEY=sk-...

# Run the proxy
QLITE_CONFIG=config/config.yaml go run ./cmd/proxy/
```

The proxy listens on `:8080` by default. Send requests just like you would to OpenAI:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

## Configuration

Config is YAML with `${ENV_VAR}` substitution:

```yaml
server:
  port: 8080
  read_timeout: 30s
  write_timeout: 120s

providers:
  - name: openai
    type: openai
    base_url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    models:
      - gpt-4o
      - gpt-4o-mini
      - gpt-4.1-nano

cache:
  exact:
    enabled: false
    ttl: 1h
    max_entries: 10000
```

Set the config path via `QLITE_CONFIG` (defaults to `config/config.yaml`).

## Cache

qlite includes an optional exact-match response cache. When enabled, identical requests return cached responses instantly with zero provider cost.

### How it works

- Cache keys are SHA-256 hashes of `model`, `messages`, `temperature`, and `top_p`
- The `stream` flag is excluded from the key — a non-streaming request populates the cache, and a subsequent streaming request can replay it as SSE
- Requests with `temperature > 0` skip the cache (non-deterministic responses shouldn't be cached)
- Non-streaming responses are stored on cache miss; streaming responses are read-only (never stored)
- Expired entries are lazily evicted on access; when at capacity, the oldest entry is evicted

### Response headers

All proxied requests include:

| Header | Values | Description |
|--------|--------|-------------|
| `X-Cache` | `HIT` / `MISS` | Whether the response came from cache |
| `X-Provider` | `cache` / provider name | Which backend served the response |
| `X-Request-Cost` | `0` on HIT | Estimated cost of the request |
| `X-Tokens-Saved` | token count (HIT only) | Tokens saved by the cache hit |

### Configuration

```yaml
cache:
  exact:
    enabled: false       # default off
    ttl: 1h              # time-to-live per entry
    max_entries: 10000   # LRU capacity
```

Set `enabled: true` to turn on. Supports `${ENV_VAR}` substitution (e.g., `enabled: ${QLITE_CACHE:-true}`).

## Architecture

```
cmd/proxy/          → main entrypoint
cmd/mockserver/     → mock OpenAI server for testing
internal/
  cache/            → exact-match response cache
  config/           → YAML config loading
  model/            → request/response types
  pipeline/         → Stage + StreamStage processing pipeline
  provider/         → provider interface + OpenAI-compatible implementation
  server/           → HTTP handler, middleware chain
  sse/              → SSE writer interface (leaf package)
  tokenizer/        → tiktoken-based token counter
  pricing/          → model pricing data
```

Requests flow through a middleware chain (RequestID, Logger, Recovery, CORS) into the handler, which dispatches through the pipeline to the appropriate provider.

## Build

```bash
go build ./cmd/proxy
go build ./cmd/mockserver
```

## Testing

```bash
# All tests
go test ./...

# Single test
go test ./internal/server -run TestName -v

# Benchmarks
go test ./internal/server -bench . -benchmem
```

## Performance

Measured with the mock server and Locust load testing. Full methodology in [`loadtest/README.md`](loadtest/README.md).

| Scenario                                    | Requests | P50 Overhead | P99 Overhead |
| ------------------------------------------- | -------- | ------------ | ------------ |
| Standard (50ms latency, 3 chunks, 20 users) | ~1,000   | ~1ms         | ~2ms         |
| Stress (5ms latency, 20 chunks, 20 users)   | ~116,000 | ~1ms         | ~1ms         |

Proxy overhead is consistently **< 3ms** across all conditions, well within the **< 10ms P99** target.

## Profiling

Enable pprof on a separate debug port:

```bash
QLITE_PPROF=1 QLITE_CONFIG=config/config.yaml go run ./cmd/proxy/
```

Then capture profiles at `http://localhost:6060/debug/pprof/`.

## License

MIT
