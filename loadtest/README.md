# Load Testing

Measures **proxy overhead** and **cost savings** during load tests.

- **Overhead** = `proxy_latency - direct_latency` (target: < 10ms P99)
- **Cost savings** = cache hit rate and dollar savings (from `X-Request-Cost` / `X-Cost-Saved` headers)

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

### Locust Test (`loadtest/locustfile.py`)

A single Locust test with 4 tasks covering both proxy overhead and cost savings.

## Locust Test Tasks

| Task | Weight | Description |
|------|--------|-------------|
| `proxy_non_streaming` | 3 | Non-streaming via proxy. Records cost headers. |
| `proxy_streaming` | 3 | Streaming via proxy. TTFB metric + cost headers. |
| `direct_non_streaming` | 1 | Direct-to-mock baseline (non-streaming). |
| `direct_streaming` | 1 | Direct-to-mock baseline (streaming TTFB). |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `QLITE_TEST_MODEL` | `gpt-4o-mini` | Model name sent in requests |
| `OPENAI_API_KEY` | `test-key` | API key header value |
| `MOCK_URL` | `http://localhost:9999` | Direct mock server URL for baseline tasks |
| `QLITE_CACHE` | `true` | Enable/disable proxy cache. Set to `false` for pure overhead measurement. Used via `${QLITE_CACHE:-true}` in `config.mock.yaml`. |

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
- **`proxy [non-stream]`** — same request routed through qlite

The difference is the proxy overhead. Target: **< 10ms**.

For streaming, compare **`TTFB [direct-stream]`** vs **`TTFB [proxy-stream]`** for time-to-first-byte overhead.

At the end of the test, a **Cost Savings Summary** prints cache hit/miss counts, hit rate, total API cost, total saved, and savings percentage.

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

# Pure overhead measurement (cache disabled)
QLITE_CACHE=false locust -f loadtest/locustfile.py --host http://localhost:8080 \
  --users 20 --spawn-rate 5 --run-time 60s --headless
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
