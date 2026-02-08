# Load Testing

Measures **proxy overhead** — the latency qlite adds on top of the upstream provider. The target is **< 10ms P99 overhead**.

```
overhead = proxy_latency - direct_latency
```

## Prerequisites

- Go 1.22+
- Python 3.8+
- Locust: `pip install locust`

## Quick Start

Run in three terminals:

```bash
# Terminal 1 — Mock OpenAI server (50ms simulated latency)
go run ./cmd/mockserver/ -port 9999 -latency 50ms

# Terminal 2 — qlite proxy pointed at mock
QLITE_CONFIG=config/config.mock.yaml go run ./cmd/proxy/

# Terminal 3 — Locust load test
locust -f loadtest/locustfile.py --host http://localhost:8080 \
  --users 20 --spawn-rate 5 --run-time 60s --headless
```

## Components

### Mock Server (`cmd/mockserver/`)

A minimal multi-provider mock server that returns fixed responses with configurable latency. Supports OpenAI, Anthropic, and Google (Gemini) API formats.

| Flag | Default | Description |
|------|---------|-------------|
| `-port` | `9999` | Listen port |
| `-latency` | `50ms` | Simulated latency. Applied once for non-streaming; applied **per-chunk** for streaming |
| `-chunks` | `3` | Number of SSE chunks for streaming (min 2: role + finish) |
| `-response-tokens` | `10` | Approximate content tokens (~5 chars each) |

Non-streaming requests return a single JSON response after one latency sleep. Streaming requests emit `-chunks` SSE chunks (role delta, content deltas, finish/usage delta), each preceded by a latency sleep, followed by `[DONE]`. Content is generated from a repeating lorem ipsum corpus sized to `-response-tokens × 5` characters, split evenly across the middle content chunks.

### Proxy (`cmd/proxy/`)

The qlite proxy reads its config from `QLITE_CONFIG` (defaults to `config/config.yaml`). For load testing, use `config/config.mock.yaml`, which routes OpenAI models (`gpt-4o`, `gpt-4o-mini`, `gpt-4.1-nano`), Anthropic models (`claude-sonnet-4-5`, `claude-haiku-4-5`), and Google models (`gemini-2.5-flash`, `gemini-2.5-pro`) to the mock server at `localhost:9999`.

### Locust Suite (`loadtest/locustfile.py`)

A Locust test suite with 6 tasks that exercise both proxied and direct (baseline) paths. Users wait 0.5–2s between tasks.

### Cache Load Test (`loadtest/cache_locustfile.py`)

A Locust test suite focused on cache performance at configurable hit rates. Set the target hit rate with the `CACHE_HIT_RATE` env var (0–100, default 0). During startup each user warms the cache by sending 10 fixed messages. During the test, each request randomly rolls against `CACHE_HIT_RATE` to decide whether to re-use a cached message or send a unique one. At test end a summary prints the actual vs target hit rate.

## Locust Test Tasks

| Task | Weight | Description |
|------|--------|-------------|
| `chat_non_streaming` | 3 | Non-streaming chat via proxy. Validates `choices` in response. |
| `chat_streaming` | 3 | Streaming chat via proxy. Consumes full SSE stream and fires a custom `TTFB [stream]` metric. |
| `chat_medium` | 2 | Non-streaming with system message via proxy. Validates `X-Cache` and `X-Provider` headers. |
| `direct_non_streaming` | 1 | Non-streaming directly to mock server (baseline). Bypasses proxy. |
| `direct_streaming` | 1 | Streaming directly to mock server (baseline). Bypasses proxy. |
| `health_check` | 1 | `GET /health` via proxy. |

## Cache Test Tasks

| Task | Weight | Description |
|------|--------|-------------|
| `cache_request` | 1 | Rolls against `CACHE_HIT_RATE` to pick a cached or unique message. Names requests `[cache-HIT]`/`[cache-MISS]` for separate Locust stats. |

### Multi-Provider Overhead Test (`loadtest/provider_locustfile.py`)

A focused Locust test that exercises all 3 provider adapter paths (OpenAI, Anthropic, Google) with cache disabled to measure pure proxy overhead for each. Includes both proxied and direct-to-mock baselines for fair comparison.

## Provider Test Tasks

| Task | Weight | Description |
|------|--------|-------------|
| `openai_non_stream` | 1 | Non-streaming via proxy, OpenAI model |
| `openai_stream` | 1 | Streaming via proxy, OpenAI model |
| `anthropic_non_stream` | 1 | Non-streaming via proxy, Anthropic model |
| `anthropic_stream` | 1 | Streaming via proxy, Anthropic model |
| `google_non_stream` | 1 | Non-streaming via proxy, Google model |
| `google_stream` | 1 | Streaming via proxy, Google model |
| `direct_openai` | 1 | Direct baseline, OpenAI mock |
| `direct_anthropic` | 1 | Direct baseline, Anthropic mock |
| `direct_google` | 1 | Direct baseline, Google mock |
| `direct_openai_stream` | 1 | Direct streaming baseline, OpenAI mock |
| `direct_anthropic_stream` | 1 | Direct streaming baseline, Anthropic mock |
| `direct_google_stream` | 1 | Direct streaming baseline, Google mock |

### Running the Provider Test

```bash
# Terminal 1 — Mock server
go run ./cmd/mockserver/ -port 9999 -latency 50ms

# Terminal 2 — Proxy with cache disabled
QLITE_CACHE=false QLITE_CONFIG=config/config.mock.yaml go run ./cmd/proxy/

# Terminal 3 — Multi-provider load test
locust -f loadtest/provider_locustfile.py --host http://localhost:8080 \
  --users 20 --spawn-rate 5 --run-time 60s --headless
```

Compare per-provider metrics: `[openai non-stream]` vs `direct [openai non-stream]`, etc. The overhead for Anthropic and Google adapters includes request/response translation cost and should be comparable to OpenAI pass-through.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `QLITE_TEST_MODEL` | `gpt-4o-mini` | Model name sent in requests |
| `OPENAI_API_KEY` | `test-key` | API key header value |
| `MOCK_URL` | `http://localhost:9999` | Direct mock server URL for baseline tasks |
| `QLITE_CACHE` | `true` | Enable/disable proxy cache. Set to `false` for pure overhead measurement. Used via `${QLITE_CACHE:-true}` in `config.mock.yaml`. |
| `QLITE_OPENAI_MODEL` | `gpt-4o-mini` | OpenAI model for provider test |
| `QLITE_ANTHROPIC_MODEL` | `claude-haiku-4-5` | Anthropic model for provider test |
| `QLITE_GOOGLE_MODEL` | `gemini-2.5-flash` | Google model for provider test |
| `CACHE_HIT_RATE` | `0` | Target cache hit rate (0–100). 0 = all misses, 100 = all hits after warmup. |

## Go Benchmarks

Unit-level P99 overhead tests that don't require Locust or the mock server binary — they spin up in-process `httptest` servers:

```bash
# Sequential: 100 requests, single goroutine
go test ./internal/server/ -run TestProxyOverhead_P99 -v

# Concurrent: 100 requests across 20 goroutines
go test ./internal/server/ -run TestProxyOverhead_P99_Concurrent -v
```

Both tests assert P99 overhead < 10ms and report P50/P99 latencies for direct and proxied paths.

## Interpreting Results

In Locust output, compare the P99 (or average) of baseline vs proxied requests:

- **`direct [non-stream]`** — baseline latency hitting the mock server directly
- **`/v1/chat/completions [non-stream]`** — same request routed through qlite

The difference is the proxy overhead. Target: **< 10ms**.

For streaming, the **`TTFB [stream]`** metric shows time-to-first-byte — the delay from sending the request to receiving the first SSE data chunk through the proxy. This captures the proxy's streaming setup overhead.

## Locust Web UI

For interactive exploration, run Locust without `--headless`:

```bash
locust -f loadtest/locustfile.py --host http://localhost:8080
```

Then open http://localhost:8089 to configure users/spawn-rate and view real-time charts.

## Common Scenarios

```bash
# Higher concurrency
locust -f loadtest/locustfile.py --host http://localhost:8080 \
  --users 50 --spawn-rate 10 --run-time 60s --headless

# Longer sustained run
locust -f loadtest/locustfile.py --host http://localhost:8080 \
  --users 20 --spawn-rate 5 --run-time 300s --headless

# Higher simulated upstream latency
go run ./cmd/mockserver/ -port 9999 -latency 100ms

# Stress test — fast upstream, many chunks
go run ./cmd/mockserver/ -port 9999 -latency 5ms -chunks 20 -response-tokens 100

# Export results to CSV
locust -f loadtest/locustfile.py --host http://localhost:8080 \
  --users 20 --spawn-rate 5 --run-time 60s --headless --csv=results

# Normal test without cache (pure overhead measurement)
QLITE_CACHE=false locust -f loadtest/locustfile.py --host http://localhost:8080 \
  --users 20 --spawn-rate 5 --run-time 60s --headless

# Cache load test — 80% hit rate
CACHE_HIT_RATE=80 locust -f loadtest/cache_locustfile.py \
  --host http://localhost:8080 --users 20 --spawn-rate 5 --run-time 60s --headless

# Cache — all misses
CACHE_HIT_RATE=0 locust -f loadtest/cache_locustfile.py \
  --host http://localhost:8080 --users 20 --spawn-rate 5 --run-time 60s --headless

# Cache — all hits (after warmup)
CACHE_HIT_RATE=100 locust -f loadtest/cache_locustfile.py \
  --host http://localhost:8080 --users 20 --spawn-rate 5 --run-time 60s --headless
```

## Benchmark Results

### Standard Test (50ms latency, 3 chunks, 20 users, 60s)

Mock server: `-latency 50ms` (default 3 chunks, 10 response-tokens)

| Metric | Direct | Proxied | Overhead |
|--------|--------|---------|----------|
| Non-stream Avg | ~50ms | ~51ms | **~1ms** |
| Stream Avg | ~152ms | ~153ms | **~1ms** |
| TTFB [stream] P50 | — | ~52ms | **~2ms** |
| P99 (non-stream) | ~55ms | ~57ms | **~2ms** |

**1,000+ requests** across 20 concurrent users. Proxy overhead consistently < 3ms.

### Stress Test (5ms latency, 20 chunks, 20 users, 60s)

Mock server: `-latency 5ms -chunks 20 -response-tokens 100`

| Metric | Direct | Proxied | Overhead |
|--------|--------|---------|----------|
| Non-stream Avg | ~5ms | ~6ms | **~1ms** |
| Stream Avg | ~102ms | ~102ms | **~0ms** |
| TTFB [stream] P50 | — | ~6ms | **~1ms** |
| P99 (non-stream) | ~9ms | ~10ms | **~1ms** |

**~116K requests** over 60s. With fast upstream latency and many chunks, the proxy adds negligible overhead — under 1ms P50 TTFB for streaming, effectively 0ms total overhead on streaming responses.

### Summary

The proxy overhead is **< 3ms** under all tested conditions, well within the **< 10ms P99** target.

## Profiling

pprof profiling is available on the proxy behind an opt-in env var.

### Enable pprof

```bash
QLITE_PPROF=1 QLITE_CONFIG=config/config.mock.yaml go run ./cmd/proxy/
```

This starts a debug server on `:6060` with pprof handlers. The debug server is separate from the main API server — no middleware interference.

### Capture CPU profile during a load test

```bash
# Start a 30-second CPU profile while Locust is running
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30
```

### Capture heap profile

```bash
go tool pprof http://localhost:6060/debug/pprof/heap
```

### View profiles in browser

```bash
# Interactive web UI for a saved profile
go tool pprof -http=:8081 profile.pb.gz

# Or view directly from the running server
go tool pprof -http=:8081 http://localhost:6060/debug/pprof/heap
```
