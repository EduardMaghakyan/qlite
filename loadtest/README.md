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

A minimal OpenAI-compatible server that returns fixed responses with configurable latency.

| Flag | Default | Description |
|------|---------|-------------|
| `-port` | `9999` | Listen port |
| `-latency` | `50ms` | Simulated latency. Applied once for non-streaming; applied **per-chunk** for streaming (3 chunks total) |

Non-streaming requests return a single JSON response after one latency sleep. Streaming requests emit 3 SSE chunks (role delta, content delta, finish/usage delta), each preceded by a latency sleep, followed by `[DONE]`.

### Proxy (`cmd/proxy/`)

The qlite proxy reads its config from `QLITE_CONFIG` (defaults to `config/config.yaml`). For load testing, use `config/config.mock.yaml`, which routes models `gpt-4o`, `gpt-4o-mini`, and `gpt-4.1-nano` to the mock server at `localhost:9999`.

### Locust Suite (`loadtest/locustfile.py`)

A Locust test suite with 6 tasks that exercise both proxied and direct (baseline) paths. Users wait 0.5–2s between tasks.

## Locust Test Tasks

| Task | Weight | Description |
|------|--------|-------------|
| `chat_non_streaming` | 3 | Non-streaming chat via proxy. Validates `choices` in response. |
| `chat_streaming` | 3 | Streaming chat via proxy. Consumes full SSE stream and fires a custom `TTFB [stream]` metric. |
| `chat_medium` | 2 | Non-streaming with system message via proxy. Validates `X-Cache` and `X-Provider` headers. |
| `direct_non_streaming` | 1 | Non-streaming directly to mock server (baseline). Bypasses proxy. |
| `direct_streaming` | 1 | Streaming directly to mock server (baseline). Bypasses proxy. |
| `health_check` | 1 | `GET /health` via proxy. |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `QLITE_TEST_MODEL` | `gpt-4o-mini` | Model name sent in requests |
| `OPENAI_API_KEY` | `test-key` | API key header value |
| `MOCK_URL` | `http://localhost:9999` | Direct mock server URL for baseline tasks |

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

# Export results to CSV
locust -f loadtest/locustfile.py --host http://localhost:8080 \
  --users 20 --spawn-rate 5 --run-time 60s --headless --csv=results
```
