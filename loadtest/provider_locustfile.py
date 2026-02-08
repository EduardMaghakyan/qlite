"""
Multi-provider load test for qlite proxy.

Exercises OpenAI, Anthropic, and Google adapter paths with cache disabled
to measure pure proxy overhead for each provider type.

Usage:
  # Terminal 1 — Mock server
  go run ./cmd/mockserver/ -port 9999 -latency 50ms

  # Terminal 2 — Proxy with cache disabled
  QLITE_CACHE=false QLITE_CONFIG=config/config.mock.yaml go run ./cmd/proxy/

  # Terminal 3 — Multi-provider load test
  locust -f loadtest/provider_locustfile.py --host http://localhost:8080 \
    --users 20 --spawn-rate 5 --run-time 60s --headless
"""

import json
import os
import time

import requests
from locust import HttpUser, between, task, events


OPENAI_MODEL = os.getenv("QLITE_OPENAI_MODEL", "gpt-4o-mini")
ANTHROPIC_MODEL = os.getenv("QLITE_ANTHROPIC_MODEL", "claude-haiku-4-5")
GOOGLE_MODEL = os.getenv("QLITE_GOOGLE_MODEL", "gemini-2.5-flash")
API_KEY = os.getenv("OPENAI_API_KEY", "test-key")
MOCK_URL = os.getenv("MOCK_URL", "http://localhost:9999")

MESSAGES = [
    {"role": "user", "content": "Say hello in one word."},
]

HEADERS = {
    "Content-Type": "application/json",
    "Authorization": f"Bearer {API_KEY}",
}


def _proxy_non_stream(client, model, name):
    """Non-streaming request through the proxy."""
    payload = {
        "model": model,
        "messages": MESSAGES,
        "max_tokens": 10,
    }
    with client.post(
        "/v1/chat/completions",
        json=payload,
        headers=HEADERS,
        catch_response=True,
        name=name,
    ) as resp:
        if resp.status_code == 200:
            body = resp.json()
            if "choices" not in body or len(body["choices"]) == 0:
                resp.failure("No choices in response")
            else:
                resp.success()
        else:
            resp.failure(f"Status {resp.status_code}: {resp.text[:200]}")


def _proxy_stream(session, host, model, name_ttfb, name_total):
    """Streaming request through the proxy with TTFB and total metrics."""
    payload = {
        "model": model,
        "messages": MESSAGES,
        "stream": True,
        "max_tokens": 10,
    }
    start = time.perf_counter()
    first_chunk_time = None
    got_done = False

    try:
        resp = session.post(
            f"{host}/v1/chat/completions",
            json=payload,
            headers=HEADERS,
            stream=True,
            timeout=10,
        )
        if resp.status_code != 200:
            elapsed_ms = (time.perf_counter() - start) * 1000
            events.request.fire(
                request_type="SSE",
                name=name_total,
                response_time=elapsed_ms,
                response_length=0,
                exception=Exception(f"Status {resp.status_code}"),
                context={},
            )
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

        end = time.perf_counter()

        if first_chunk_time is not None:
            ttfb_ms = (first_chunk_time - start) * 1000
            events.request.fire(
                request_type="SSE",
                name=name_ttfb,
                response_time=ttfb_ms,
                response_length=0,
                exception=None,
                context={},
            )

        total_ms = (end - start) * 1000
        events.request.fire(
            request_type="SSE",
            name=name_total,
            response_time=total_ms,
            response_length=0,
            exception=None if got_done else Exception("No [DONE] marker"),
            context={},
        )
    except Exception as e:
        elapsed_ms = (time.perf_counter() - start) * 1000
        events.request.fire(
            request_type="SSE",
            name=name_total,
            response_time=elapsed_ms,
            response_length=0,
            exception=e,
            context={},
        )


def _direct_non_stream(session, url, payload, name):
    """Direct-to-mock non-streaming baseline."""
    start = time.perf_counter()
    try:
        resp = session.post(url, json=payload, headers=HEADERS, timeout=10)
        elapsed_ms = (time.perf_counter() - start) * 1000
        events.request.fire(
            request_type="POST",
            name=name,
            response_time=elapsed_ms,
            response_length=len(resp.content),
            exception=None if resp.status_code == 200 else Exception(f"Status {resp.status_code}"),
            context={},
        )
    except Exception as e:
        elapsed_ms = (time.perf_counter() - start) * 1000
        events.request.fire(
            request_type="POST",
            name=name,
            response_time=elapsed_ms,
            response_length=0,
            exception=e,
            context={},
        )


def _direct_stream(session, url, payload, name_ttfb, name_total, has_done_marker=True):
    """Direct-to-mock streaming baseline."""
    start = time.perf_counter()
    first_chunk_time = None

    try:
        resp = session.post(url, json=payload, headers=HEADERS, stream=True, timeout=10)
        if resp.status_code != 200:
            elapsed_ms = (time.perf_counter() - start) * 1000
            events.request.fire(
                request_type="POST",
                name=name_total,
                response_time=elapsed_ms,
                response_length=0,
                exception=Exception(f"Status {resp.status_code}"),
                context={},
            )
            return

        stream_ok = False
        for line in resp.iter_lines():
            if not line:
                continue
            decoded = line.decode("utf-8") if isinstance(line, bytes) else line
            if decoded.startswith("data: "):
                data = decoded[6:]
                if data == "[DONE]":
                    stream_ok = True
                    break
                if first_chunk_time is None:
                    first_chunk_time = time.perf_counter()

        # For providers without [DONE] marker (Google), stream ends when connection closes.
        if not has_done_marker:
            stream_ok = True

        elapsed_ms = (time.perf_counter() - start) * 1000
        events.request.fire(
            request_type="POST",
            name=name_total,
            response_time=elapsed_ms,
            response_length=0,
            exception=None if stream_ok else Exception("Stream incomplete"),
            context={},
        )

        if first_chunk_time is not None:
            ttfb_ms = (first_chunk_time - start) * 1000
            events.request.fire(
                request_type="SSE",
                name=name_ttfb,
                response_time=ttfb_ms,
                response_length=0,
                exception=None,
                context={},
            )
    except Exception as e:
        elapsed_ms = (time.perf_counter() - start) * 1000
        events.request.fire(
            request_type="POST",
            name=name_total,
            response_time=elapsed_ms,
            response_length=0,
            exception=e,
            context={},
        )


class ProviderUser(HttpUser):
    wait_time = between(0.5, 2)

    def on_start(self):
        adapter = requests.adapters.HTTPAdapter(
            pool_connections=10, pool_maxsize=10
        )
        self.direct_session = requests.Session()
        self.direct_session.mount("http://", adapter)
        self.direct_session.mount("https://", adapter)

    # --- Proxied non-streaming ---

    @task(1)
    def openai_non_stream(self):
        _proxy_non_stream(self.client, OPENAI_MODEL, "[openai non-stream]")

    @task(1)
    def anthropic_non_stream(self):
        _proxy_non_stream(self.client, ANTHROPIC_MODEL, "[anthropic non-stream]")

    @task(1)
    def google_non_stream(self):
        _proxy_non_stream(self.client, GOOGLE_MODEL, "[google non-stream]")

    # --- Proxied streaming ---

    @task(1)
    def openai_stream(self):
        _proxy_stream(
            self.direct_session, self.host, OPENAI_MODEL,
            "TTFB [openai stream]", "total [openai stream]",
        )

    @task(1)
    def anthropic_stream(self):
        _proxy_stream(
            self.direct_session, self.host, ANTHROPIC_MODEL,
            "TTFB [anthropic stream]", "total [anthropic stream]",
        )

    @task(1)
    def google_stream(self):
        _proxy_stream(
            self.direct_session, self.host, GOOGLE_MODEL,
            "TTFB [google stream]", "total [google stream]",
        )

    # --- Direct baselines (non-streaming) ---

    @task(1)
    def direct_openai(self):
        payload = {"model": OPENAI_MODEL, "messages": MESSAGES, "max_tokens": 10}
        _direct_non_stream(
            self.direct_session,
            f"{MOCK_URL}/v1/chat/completions",
            payload,
            "direct [openai non-stream]",
        )

    @task(1)
    def direct_anthropic(self):
        payload = {"model": ANTHROPIC_MODEL, "messages": MESSAGES, "max_tokens": 10}
        _direct_non_stream(
            self.direct_session,
            f"{MOCK_URL}/v1/messages",
            payload,
            "direct [anthropic non-stream]",
        )

    @task(1)
    def direct_google(self):
        payload = {
            "contents": [{"role": "user", "parts": [{"text": "Say hello in one word."}]}],
            "generationConfig": {"maxOutputTokens": 10},
        }
        _direct_non_stream(
            self.direct_session,
            f"{MOCK_URL}/v1beta/models/{GOOGLE_MODEL}:generateContent",
            payload,
            "direct [google non-stream]",
        )

    # --- Direct baselines (streaming) ---

    @task(1)
    def direct_openai_stream(self):
        payload = {"model": OPENAI_MODEL, "messages": MESSAGES, "stream": True, "max_tokens": 10}
        _direct_stream(
            self.direct_session,
            f"{MOCK_URL}/v1/chat/completions",
            payload,
            "TTFB [direct openai stream]",
            "total [direct openai stream]",
            has_done_marker=True,
        )

    @task(1)
    def direct_anthropic_stream(self):
        payload = {"model": ANTHROPIC_MODEL, "messages": MESSAGES, "stream": True, "max_tokens": 10}
        _direct_stream(
            self.direct_session,
            f"{MOCK_URL}/v1/messages",
            payload,
            "TTFB [direct anthropic stream]",
            "total [direct anthropic stream]",
            has_done_marker=False,
        )

    @task(1)
    def direct_google_stream(self):
        payload = {
            "contents": [{"role": "user", "parts": [{"text": "Say hello in one word."}]}],
            "generationConfig": {"maxOutputTokens": 10},
        }
        _direct_stream(
            self.direct_session,
            f"{MOCK_URL}/v1beta/models/{GOOGLE_MODEL}:streamGenerateContent",
            payload,
            "TTFB [direct google stream]",
            "total [direct google stream]",
            has_done_marker=False,
        )
