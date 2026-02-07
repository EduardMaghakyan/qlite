"""
Cache-focused load test for qlite proxy.

Exercises the cache at configurable hit rates to measure cache performance.

Usage:
  # Terminal 1: mock server
  go run ./cmd/mockserver -port 9999 -latency 50ms

  # Terminal 2: proxy
  QLITE_CONFIG=config/config.mock.yaml go run ./cmd/proxy

  # Terminal 3: load test — 80% cache hit rate
  CACHE_HIT_RATE=80 locust -f loadtest/cache_locustfile.py \
    --host http://localhost:8080 --users 20 --spawn-rate 5 --run-time 60s --headless

  # 0% hit rate (all misses)
  CACHE_HIT_RATE=0 locust -f loadtest/cache_locustfile.py \
    --host http://localhost:8080 --users 20 --spawn-rate 5 --run-time 60s --headless

  # 100% hit rate (all from cache after warmup)
  CACHE_HIT_RATE=100 locust -f loadtest/cache_locustfile.py \
    --host http://localhost:8080 --users 20 --spawn-rate 5 --run-time 60s --headless
"""

import os
import random
import threading

from locust import HttpUser, between, task, events


MODEL = os.getenv("QLITE_TEST_MODEL", "gpt-4o-mini")
API_KEY = os.getenv("OPENAI_API_KEY", "test-key")
CACHE_HIT_RATE = int(os.getenv("CACHE_HIT_RATE", "0"))

HEADERS = {
    "Content-Type": "application/json",
    "Authorization": f"Bearer {API_KEY}",
}

# 10 fixed message sets that will be warmed into cache.
CACHE_POOL = [
    [{"role": "user", "content": f"Cache test message number {i}"}]
    for i in range(10)
]

# Thread-safe atomic counter for generating unique miss messages.
_miss_counter = 0
_miss_lock = threading.Lock()


def next_miss_id():
    global _miss_counter
    with _miss_lock:
        _miss_counter += 1
        return _miss_counter


# Track actual HIT/MISS from X-Cache header.
_hit_count = 0
_miss_count = 0
_stats_lock = threading.Lock()


def record_cache_result(header_value):
    global _hit_count, _miss_count
    with _stats_lock:
        if header_value == "HIT":
            _hit_count += 1
        else:
            _miss_count += 1


@events.test_stop.add_listener
def on_test_stop(environment, **kwargs):
    total = _hit_count + _miss_count
    if total == 0:
        print("\n=== Cache Summary ===")
        print("No cache requests recorded.")
        return
    actual_rate = (_hit_count / total) * 100
    print("\n=== Cache Summary ===")
    print(f"Target hit rate:  {CACHE_HIT_RATE}%")
    print(f"Actual hits:      {_hit_count}")
    print(f"Actual misses:    {_miss_count}")
    print(f"Total requests:   {total}")
    print(f"Actual hit rate:  {actual_rate:.1f}%")
    print("=====================\n")


class CacheUser(HttpUser):
    wait_time = between(0.5, 2)

    def on_start(self):
        """Warmup: send all 10 pool messages to populate cache."""
        for messages in CACHE_POOL:
            payload = {
                "model": MODEL,
                "messages": messages,
                "max_tokens": 10,
            }
            self.client.post(
                "/v1/chat/completions",
                json=payload,
                headers=HEADERS,
                name="[warmup]",
            )

    @task
    def cache_request(self):
        """Send a request that is either a cache HIT or MISS based on CACHE_HIT_RATE."""
        if random.randint(1, 100) <= CACHE_HIT_RATE:
            messages = random.choice(CACHE_POOL)
            expected = "HIT"
        else:
            uid = next_miss_id()
            messages = [{"role": "user", "content": f"Unique miss message {uid}"}]
            expected = "MISS"

        payload = {
            "model": MODEL,
            "messages": messages,
            "max_tokens": 10,
        }

        with self.client.post(
            "/v1/chat/completions",
            json=payload,
            headers=HEADERS,
            catch_response=True,
            name=f"[cache-{expected}]",
        ) as resp:
            if resp.status_code == 200:
                cache_header = resp.headers.get("X-Cache", "MISS")
                record_cache_result(cache_header)
                body = resp.json()
                if "choices" not in body or len(body["choices"]) == 0:
                    resp.failure("No choices in response")
                else:
                    resp.success()
            else:
                record_cache_result("MISS")
                resp.failure(f"Status {resp.status_code}: {resp.text[:200]}")
