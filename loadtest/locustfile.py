"""
Locust load test for qlite proxy.

Usage:
  # Start qlite proxy first:
  #   OPENAI_API_KEY=sk-... go run ./cmd/proxy/

  # Run headless (CLI):
  locust -f loadtest/locustfile.py --host http://localhost:8080 \
    --users 10 --spawn-rate 2 --run-time 30s --headless

  # Run with web UI:
  locust -f loadtest/locustfile.py --host http://localhost:8080
  # Then open http://localhost:8089

  # --- Mock-based overhead measurement ---
  # Terminal 1: go run ./cmd/mockserver/ -port 9999 -latency 50ms
  # Terminal 2: QLITE_CONFIG=config/config.mock.yaml go run ./cmd/proxy/
  # Terminal 3: locust -f loadtest/locustfile.py --host http://localhost:8080 \
  #               --users 20 --spawn-rate 5 --run-time 60s --headless
  # Compare "direct [non-stream]" avg vs "/v1/chat/completions [non-stream]" avg
"""

import json
import os
import time

import requests
from locust import HttpUser, between, task, events


MODEL = os.getenv("QLITE_TEST_MODEL", "gpt-4o-mini")
API_KEY = os.getenv("OPENAI_API_KEY", "test-key")
MOCK_URL = os.getenv("MOCK_URL", "http://localhost:9999")

MESSAGES_SHORT = [
    {"role": "user", "content": "Say hello in one word."},
]

MESSAGES_MEDIUM = [
    {"role": "system", "content": "You are a concise assistant. Reply in one sentence."},
    {"role": "user", "content": "What is the capital of France?"},
]

HEADERS = {
    "Content-Type": "application/json",
    "Authorization": f"Bearer {API_KEY}",
}


class QliteUser(HttpUser):
    wait_time = between(0.5, 2)

    @task(3)
    def chat_non_streaming(self):
        """Non-streaming chat completion."""
        payload = {
            "model": MODEL,
            "messages": MESSAGES_SHORT,
            "max_tokens": 10,
        }
        with self.client.post(
            "/v1/chat/completions",
            json=payload,
            headers=HEADERS,
            catch_response=True,
            name="/v1/chat/completions [non-stream]",
        ) as resp:
            if resp.status_code == 200:
                body = resp.json()
                if "choices" not in body or len(body["choices"]) == 0:
                    resp.failure("No choices in response")
                else:
                    resp.success()
            else:
                resp.failure(f"Status {resp.status_code}: {resp.text[:200]}")

    @task(3)
    def chat_streaming(self):
        """Streaming chat completion — measures time-to-first-byte and full stream."""
        payload = {
            "model": MODEL,
            "messages": MESSAGES_SHORT,
            "stream": True,
            "max_tokens": 10,
        }
        start = time.perf_counter()
        first_chunk_time = None
        chunk_count = 0
        got_done = False

        with self.client.post(
            "/v1/chat/completions",
            json=payload,
            headers=HEADERS,
            stream=True,
            catch_response=True,
            name="/v1/chat/completions [stream]",
        ) as resp:
            if resp.status_code != 200:
                resp.failure(f"Status {resp.status_code}: {resp.text[:200]}")
                return

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
                    chunk_count += 1

            if not got_done:
                resp.failure("Stream did not end with [DONE]")
            elif chunk_count == 0:
                resp.failure("No data chunks received")
            else:
                resp.success()

        # Fire custom TTFB metric.
        if first_chunk_time is not None:
            ttfb_ms = (first_chunk_time - start) * 1000
            events.request.fire(
                request_type="SSE",
                name="TTFB [stream]",
                response_time=ttfb_ms,
                response_length=0,
                exception=None,
                context={},
            )

    @task(2)
    def chat_medium(self):
        """Non-streaming with a system message."""
        payload = {
            "model": MODEL,
            "messages": MESSAGES_MEDIUM,
            "max_tokens": 30,
        }
        with self.client.post(
            "/v1/chat/completions",
            json=payload,
            headers=HEADERS,
            catch_response=True,
            name="/v1/chat/completions [medium]",
        ) as resp:
            if resp.status_code == 200:
                body = resp.json()
                # Verify proxy headers are present.
                cache = resp.headers.get("X-Cache")
                provider = resp.headers.get("X-Provider")
                if not cache:
                    resp.failure("Missing X-Cache header")
                elif not provider:
                    resp.failure("Missing X-Provider header")
                elif "choices" not in body:
                    resp.failure("No choices in response")
                else:
                    resp.success()
            else:
                resp.failure(f"Status {resp.status_code}: {resp.text[:200]}")

    @task(1)
    def direct_non_streaming(self):
        """Direct-to-mock baseline (non-streaming) — bypasses proxy."""
        payload = {
            "model": MODEL,
            "messages": MESSAGES_SHORT,
            "max_tokens": 10,
        }
        start = time.perf_counter()
        try:
            resp = requests.post(
                f"{MOCK_URL}/v1/chat/completions",
                json=payload,
                headers=HEADERS,
                timeout=10,
            )
            elapsed_ms = (time.perf_counter() - start) * 1000
            if resp.status_code == 200:
                events.request.fire(
                    request_type="POST",
                    name="direct [non-stream]",
                    response_time=elapsed_ms,
                    response_length=len(resp.content),
                    exception=None,
                    context={},
                )
            else:
                events.request.fire(
                    request_type="POST",
                    name="direct [non-stream]",
                    response_time=elapsed_ms,
                    response_length=0,
                    exception=Exception(f"Status {resp.status_code}"),
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
            "messages": MESSAGES_SHORT,
            "stream": True,
            "max_tokens": 10,
        }
        start = time.perf_counter()
        try:
            resp = requests.post(
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
                    name="direct [stream]",
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
                if decoded == "data: [DONE]":
                    got_done = True
                    break

            elapsed_ms = (time.perf_counter() - start) * 1000
            if got_done:
                events.request.fire(
                    request_type="POST",
                    name="direct [stream]",
                    response_time=elapsed_ms,
                    response_length=0,
                    exception=None,
                    context={},
                )
            else:
                events.request.fire(
                    request_type="POST",
                    name="direct [stream]",
                    response_time=elapsed_ms,
                    response_length=0,
                    exception=Exception("No [DONE] marker"),
                    context={},
                )
        except Exception as e:
            elapsed_ms = (time.perf_counter() - start) * 1000
            events.request.fire(
                request_type="POST",
                name="direct [stream]",
                response_time=elapsed_ms,
                response_length=0,
                exception=e,
                context={},
            )

    @task(1)
    def health_check(self):
        """Health endpoint — should always be fast."""
        with self.client.get(
            "/health",
            catch_response=True,
            name="/health",
        ) as resp:
            if resp.status_code == 200:
                resp.success()
            else:
                resp.failure(f"Status {resp.status_code}")
