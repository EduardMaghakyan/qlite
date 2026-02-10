"""
Locust load test for qlite proxy.

Measures two things:
  1. Proxy overhead — direct vs proxied latency
  2. Cost savings — cache hit rate and dollar savings

Usage:
  # --- Mock-based overhead measurement ---
  # Terminal 1: go run ./cmd/mockserver/ -port 9999 -latency 50ms
  # Terminal 2: QLITE_CONFIG=config/config.mock.yaml go run ./cmd/proxy/
  # Terminal 3:
  locust -f loadtest/locustfile.py --host http://localhost:8080 \
    --users 20 --spawn-rate 5 --run-time 60s --headless

  # Compare "direct [non-stream]" avg vs "proxy [non-stream]" avg for overhead.

  # --- Stress test (reveal proxy overhead) ---
  # Terminal 1: go run ./cmd/mockserver/ -port 9999 -latency 5ms -chunks 20 -response-tokens 100
  # Terminal 2: QLITE_CONFIG=config/config.mock.yaml go run ./cmd/proxy/
  # Terminal 3:
  locust -f loadtest/locustfile.py --host http://localhost:8080 \
    --users 5 --spawn-rate 1 --run-time 120s --headless
"""

import os
import threading
import time

import requests
from locust import HttpUser, between, task, events


MODEL = os.getenv("QLITE_TEST_MODEL", "gpt-4o-mini")
API_KEY = os.getenv("OPENAI_API_KEY", "test-key")
MOCK_URL = os.getenv("MOCK_URL", "http://localhost:9999")

MESSAGES = [
    {"role": "user", "content": "Say hello in one word."},
]

HEADERS = {
    "Content-Type": "application/json",
    "Authorization": f"Bearer {API_KEY}",
}

# ---------------------------------------------------------------------------
# Thread-safe cost & cache counters
# ---------------------------------------------------------------------------

_total_cost = 0.0
_total_saved = 0.0
_cache_hits = 0
_cache_misses = 0
_stats_lock = threading.Lock()


def record_cost(cache_header, cost, cost_saved):
    global _total_cost, _total_saved, _cache_hits, _cache_misses
    with _stats_lock:
        _total_cost += cost
        _total_saved += cost_saved
        if cache_header == "HIT":
            _cache_hits += 1
        else:
            _cache_misses += 1


@events.test_stop.add_listener
def on_test_stop(environment, **kwargs):
    total = _cache_hits + _cache_misses
    if total == 0:
        print("\n=== Cost Savings Summary ===")
        print("No proxy requests recorded.")
        print("============================\n")
        return

    hit_rate = (_cache_hits / total) * 100
    cost_without_cache = _total_cost + _total_saved
    savings_pct = (_total_saved / cost_without_cache * 100) if cost_without_cache > 0 else 0.0

    print("\n=== Cost Savings Summary ===")
    print(f"Cache hits:              {_cache_hits}")
    print(f"Cache misses:            {_cache_misses}")
    print(f"Hit rate:                {hit_rate:.1f}%")
    print(f"Total API cost (actual): ${_total_cost:.8f}")
    print(f"Total saved:             ${_total_saved:.8f}")
    print(f"Cost without cache:      ${cost_without_cache:.8f}")
    print(f"Savings:                 {savings_pct:.1f}%")
    print("============================\n")


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _record_proxy_cost(resp):
    """Extract cost headers from a proxy response and record them."""
    cache_header = resp.headers.get("X-Cache", "MISS")
    cost = float(resp.headers.get("X-Request-Cost", "0"))
    cost_saved = float(resp.headers.get("X-Cost-Saved", "0"))
    record_cost(cache_header, cost, cost_saved)


# ---------------------------------------------------------------------------
# Locust user
# ---------------------------------------------------------------------------


class QliteUser(HttpUser):
    wait_time = between(0.5, 2)

    def on_start(self):
        adapter = requests.adapters.HTTPAdapter(
            pool_connections=10, pool_maxsize=10
        )
        self.direct_session = requests.Session()
        self.direct_session.mount("http://", adapter)
        self.direct_session.mount("https://", adapter)

    # --- Proxied tasks ---

    @task(3)
    def proxy_non_streaming(self):
        """Non-streaming chat completion via proxy."""
        payload = {
            "model": MODEL,
            "messages": MESSAGES,
            "max_tokens": 10,
        }
        with self.client.post(
            "/v1/chat/completions",
            json=payload,
            headers=HEADERS,
            catch_response=True,
            name="proxy [non-stream]",
        ) as resp:
            if resp.status_code == 200:
                _record_proxy_cost(resp)
                body = resp.json()
                if "choices" not in body or len(body["choices"]) == 0:
                    resp.failure("No choices in response")
                else:
                    resp.success()
            else:
                resp.failure(f"Status {resp.status_code}: {resp.text[:200]}")

    @task(3)
    def proxy_streaming(self):
        """Streaming chat completion via proxy. Measures TTFB and total time."""
        payload = {
            "model": MODEL,
            "messages": MESSAGES,
            "stream": True,
            "max_tokens": 10,
        }
        start = time.perf_counter()
        first_chunk_time = None
        got_done = False

        try:
            resp = self.direct_session.post(
                f"{self.host}/v1/chat/completions",
                json=payload,
                headers=HEADERS,
                stream=True,
                timeout=10,
            )
            if resp.status_code != 200:
                elapsed_ms = (time.perf_counter() - start) * 1000
                events.request.fire(
                    request_type="SSE",
                    name="total [proxy-stream]",
                    response_time=elapsed_ms,
                    response_length=0,
                    exception=Exception(f"Status {resp.status_code}"),
                    context={},
                )
                return

            _record_proxy_cost(resp)

            for line in resp.iter_lines():
                if not line:
                    continue
                decoded = line.decode("utf-8") if isinstance(line, bytes) else line
                if decoded.startswith("data: "):
                    data = decoded[6:]
                    if data == "[DONE]":
                        got_done = True
                        break
                    if first_chunk_time is None:
                        first_chunk_time = time.perf_counter()

            end = time.perf_counter()

            if first_chunk_time is not None:
                ttfb_ms = (first_chunk_time - start) * 1000
                events.request.fire(
                    request_type="SSE",
                    name="TTFB [proxy-stream]",
                    response_time=ttfb_ms,
                    response_length=0,
                    exception=None,
                    context={},
                )

            total_ms = (end - start) * 1000
            events.request.fire(
                request_type="SSE",
                name="total [proxy-stream]",
                response_time=total_ms,
                response_length=0,
                exception=None if got_done else Exception("No [DONE] marker"),
                context={},
            )
        except Exception as e:
            elapsed_ms = (time.perf_counter() - start) * 1000
            events.request.fire(
                request_type="SSE",
                name="total [proxy-stream]",
                response_time=elapsed_ms,
                response_length=0,
                exception=e,
                context={},
            )

    # --- Direct baseline tasks ---

    @task(1)
    def direct_non_streaming(self):
        """Direct-to-mock baseline (non-streaming) — bypasses proxy."""
        payload = {
            "model": MODEL,
            "messages": MESSAGES,
            "max_tokens": 10,
        }
        start = time.perf_counter()
        try:
            resp = self.direct_session.post(
                f"{MOCK_URL}/v1/chat/completions",
                json=payload,
                headers=HEADERS,
                timeout=10,
            )
            elapsed_ms = (time.perf_counter() - start) * 1000
            events.request.fire(
                request_type="POST",
                name="direct [non-stream]",
                response_time=elapsed_ms,
                response_length=len(resp.content),
                exception=None if resp.status_code == 200 else Exception(f"Status {resp.status_code}"),
                context={},
            )
        except Exception as e:
            elapsed_ms = (time.perf_counter() - start) * 1000
            events.request.fire(
                request_type="POST",
                name="direct [non-stream]",
                response_time=elapsed_ms,
                response_length=0,
                exception=e,
                context={},
            )

    @task(1)
    def direct_streaming(self):
        """Direct-to-mock baseline (streaming) — bypasses proxy."""
        payload = {
            "model": MODEL,
            "messages": MESSAGES,
            "stream": True,
            "max_tokens": 10,
        }
        start = time.perf_counter()
        first_chunk_time = None
        try:
            resp = self.direct_session.post(
                f"{MOCK_URL}/v1/chat/completions",
                json=payload,
                headers=HEADERS,
                stream=True,
                timeout=10,
            )
            if resp.status_code != 200:
                elapsed_ms = (time.perf_counter() - start) * 1000
                events.request.fire(
                    request_type="POST",
                    name="total [direct-stream]",
                    response_time=elapsed_ms,
                    response_length=0,
                    exception=Exception(f"Status {resp.status_code}"),
                    context={},
                )
                return

            got_done = False
            for line in resp.iter_lines():
                if not line:
                    continue
                decoded = line.decode("utf-8") if isinstance(line, bytes) else line
                if decoded.startswith("data: "):
                    data = decoded[6:]
                    if data == "[DONE]":
                        got_done = True
                        break
                    if first_chunk_time is None:
                        first_chunk_time = time.perf_counter()

            elapsed_ms = (time.perf_counter() - start) * 1000
            events.request.fire(
                request_type="POST",
                name="total [direct-stream]",
                response_time=elapsed_ms,
                response_length=0,
                exception=None if got_done else Exception("No [DONE] marker"),
                context={},
            )

            if first_chunk_time is not None:
                ttfb_ms = (first_chunk_time - start) * 1000
                events.request.fire(
                    request_type="SSE",
                    name="TTFB [direct-stream]",
                    response_time=ttfb_ms,
                    response_length=0,
                    exception=None,
                    context={},
                )
        except Exception as e:
            elapsed_ms = (time.perf_counter() - start) * 1000
            events.request.fire(
                request_type="POST",
                name="total [direct-stream]",
                response_time=elapsed_ms,
                response_length=0,
                exception=e,
                context={},
            )
