"""
Semantic cache load test for qlite proxy.

Exercises exact cache, semantic cache, and cache misses at configurable rates.
Reports hit-rate stats and cost savings summary at the end of the run.

Prerequisites:
  docker run -d -p 6333:6333 qdrant/qdrant
  QLITE_CONFIG=config/config.yaml OPENAI_API_KEY=... go run ./cmd/proxy

Usage:
  # Default rates: 20% exact, 50% semantic, 30% miss
  OPENAI_API_KEY=sk-... locust -f loadtest/semantic_locustfile.py \
    --host http://localhost:8080 --users 10 --spawn-rate 2 --run-time 60s --headless

  # Custom rates (must sum to <= 100)
  EXACT_HIT_RATE=10 SEMANTIC_HIT_RATE=60 OPENAI_API_KEY=sk-... \
    locust -f loadtest/semantic_locustfile.py \
    --host http://localhost:8080 --users 20 --spawn-rate 5 --run-time 60s --headless

Env vars:
  OPENAI_API_KEY     — required
  QLITE_TEST_MODEL   — default gpt-4o-mini
  EXACT_HIT_RATE     — 0-100, default 20
  SEMANTIC_HIT_RATE  — 0-100, default 50
"""

import os
import random
import threading
import time

import requests as req_lib
from locust import HttpUser, between, task, events


MODEL = os.getenv("QLITE_TEST_MODEL", "gpt-4o-mini")
API_KEY = os.getenv("OPENAI_API_KEY", "test-key")
EXACT_HIT_RATE = int(os.getenv("EXACT_HIT_RATE", "20"))
SEMANTIC_HIT_RATE = int(os.getenv("SEMANTIC_HIT_RATE", "50"))

HEADERS = {
    "Content-Type": "application/json",
    "Authorization": f"Bearer {API_KEY}",
}

# ---------------------------------------------------------------------------
# Clear caches at test start (before any user warmup)
# ---------------------------------------------------------------------------


@events.test_start.add_listener
def on_test_start(environment, **kwargs):
    """Clear exact cache and Qdrant collection before test begins."""
    host = environment.host or "http://localhost:8080"
    try:
        resp = req_lib.post(f"{host}/admin/cache/clear")
        if resp.status_code == 200:
            print("[setup] Caches cleared successfully")
        else:
            print(f"[setup] WARNING: cache clear failed (status {resp.status_code})")
    except Exception as e:
        print(f"[setup] WARNING: cache clear request failed: {e}")


# ---------------------------------------------------------------------------
# Message pools
# ---------------------------------------------------------------------------

# Semantic pool: (seed_message, [variant_messages])
# Seeds are sent during warmup to populate both exact and semantic caches.
# Variants are close paraphrases to reliably hit at 0.95 threshold.
SEMANTIC_POOL = [
    (
        "Say hello in exactly three words",
        [
            "Say hello in precisely three words",
            "Say hello using exactly three words",
        ],
    ),
    (
        "What is the capital of France?",
        [
            "What is the capital city of France?",
        ],
    ),
    (
        "Count from 1 to 5",
        [
            "Count from one to five",
            "Count from 1 up to 5",
        ],
    ),
    (
        "What color is the sky?",
        [
            "What colour is the sky?",
            "What color is the sky on a clear day?",
        ],
    ),
    (
        "Name three fruits",
        [
            "Name 3 fruits",
            "Name three common fruits",
        ],
    ),
]

# Exact pool: fixed messages for exact cache hits (same as cache_locustfile.py pattern).
EXACT_POOL = [
    [{"role": "user", "content": f"Exact cache test message number {i}"}]
    for i in range(10)
]

# ---------------------------------------------------------------------------
# Thread-safe counters
# ---------------------------------------------------------------------------

# Topically distinct messages so misses don't semantically match each other.
MISS_TOPICS = [
    "Explain the Pythagorean theorem",
    "What is photosynthesis?",
    "Describe the water cycle",
    "Who invented the telephone?",
    "What is the speed of light?",
    "Define opportunity cost",
    "What causes earthquakes?",
    "Explain how vaccines work",
    "What is the Fibonacci sequence?",
    "Describe the structure of DNA",
]

_miss_counter = 0
_miss_lock = threading.Lock()


def next_miss_id():
    global _miss_counter
    with _miss_lock:
        _miss_counter += 1
        return _miss_counter


# Three-way classification counters.
_exact_hits = 0
_semantic_hits = 0
_misses = 0

# Cost accumulators.
_total_cost = 0.0
_total_saved = 0.0
_exact_saved = 0.0
_semantic_saved = 0.0

_stats_lock = threading.Lock()


def record_result(cache_header, provider_header, cost, cost_saved):
    global _exact_hits, _semantic_hits, _misses
    global _total_cost, _total_saved, _exact_saved, _semantic_saved
    with _stats_lock:
        _total_cost += cost
        if cache_header == "HIT" and provider_header == "semantic_cache":
            _semantic_hits += 1
            _total_saved += cost_saved
            _semantic_saved += cost_saved
        elif cache_header == "HIT" and provider_header == "cache":
            _exact_hits += 1
            _total_saved += cost_saved
            _exact_saved += cost_saved
        else:
            _misses += 1


# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------


@events.test_stop.add_listener
def on_test_stop(environment, **kwargs):
    total = _exact_hits + _semantic_hits + _misses
    if total == 0:
        print("\n=== Semantic Cache Summary ===")
        print("No requests recorded.")
        return
    miss_rate = 100 - EXACT_HIT_RATE - SEMANTIC_HIT_RATE
    print("\n=== Semantic Cache Summary ===")
    print(
        f"Target rates:  exact={EXACT_HIT_RATE}%  "
        f"semantic={SEMANTIC_HIT_RATE}%  miss={miss_rate}%"
    )
    print(f"Exact hits:    {_exact_hits} ({_exact_hits / total * 100:.1f}%)")
    print(f"Semantic hits: {_semantic_hits} ({_semantic_hits / total * 100:.1f}%)")
    print(f"Misses:        {_misses} ({_misses / total * 100:.1f}%)")
    print(f"Total:         {total}")
    print("===============================\n")

    cost_without_cache = _total_cost + _total_saved
    savings_pct = (_total_saved / cost_without_cache * 100) if cost_without_cache > 0 else 0.0
    print("=== Cost Savings Summary ===")
    print(f"Total API cost (actual):    ${_total_cost:.8f}")
    print(f"Saved by exact cache:       ${_exact_saved:.8f}  ({_exact_hits} hits)")
    print(f"Saved by semantic cache:    ${_semantic_saved:.8f}  ({_semantic_hits} hits)")
    print(f"Total saved:                ${_total_saved:.8f}")
    print(f"Cost without cache:         ${cost_without_cache:.8f}")
    print(f"Savings:                    {savings_pct:.1f}%")
    print("============================\n")


# ---------------------------------------------------------------------------
# Locust user
# ---------------------------------------------------------------------------


class SemanticCacheUser(HttpUser):
    wait_time = between(0.5, 2)

    def on_start(self):
        """Warmup: populate exact cache and seed semantic cache via Qdrant."""
        # 1. Seed exact pool.
        for messages in EXACT_POOL:
            payload = {
                "model": MODEL,
                "messages": messages,
                "max_tokens": 10,
            }
            self.client.post(
                "/v1/chat/completions",
                json=payload,
                headers=HEADERS,
                name="[warmup-exact]",
            )

        # 2. Seed semantic pool (triggers async Qdrant store).
        for seed_msg, _ in SEMANTIC_POOL:
            payload = {
                "model": MODEL,
                "messages": [{"role": "user", "content": seed_msg}],
                "max_tokens": 10,
            }
            self.client.post(
                "/v1/chat/completions",
                json=payload,
                headers=HEADERS,
                name="[warmup-semantic]",
            )

        # 3. Wait for async Qdrant upserts to complete.
        time.sleep(3)

    @task
    def cache_request(self):
        """Send a request targeting exact hit, semantic hit, or miss."""
        roll = random.randint(1, 100)

        if roll <= EXACT_HIT_RATE:
            # Exact cache hit — reuse a pool message verbatim.
            messages = random.choice(EXACT_POOL)
            req_name = "[cache-exact-HIT]"
        elif roll <= EXACT_HIT_RATE + SEMANTIC_HIT_RATE:
            # Semantic cache hit — use a variant of a seeded message.
            _, variants = random.choice(SEMANTIC_POOL)
            variant = random.choice(variants)
            messages = [{"role": "user", "content": variant}]
            req_name = "[cache-semantic-HIT]"
        else:
            # Cache miss — topically distinct message so misses don't match each other.
            uid = next_miss_id()
            topic = MISS_TOPICS[uid % len(MISS_TOPICS)]
            messages = [{"role": "user", "content": f"{topic} (variant {uid})"}]
            req_name = "[cache-MISS]"

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
            name=req_name,
        ) as resp:
            if resp.status_code == 200:
                cache_header = resp.headers.get("X-Cache", "MISS")
                provider_header = resp.headers.get("X-Provider", "")
                cost = float(resp.headers.get("X-Request-Cost", "0"))
                cost_saved = float(resp.headers.get("X-Cost-Saved", "0"))
                record_result(cache_header, provider_header, cost, cost_saved)
                body = resp.json()
                if "choices" not in body or len(body["choices"]) == 0:
                    resp.failure("No choices in response")
                else:
                    resp.success()
            else:
                record_result("MISS", "", 0.0, 0.0)
                resp.failure(f"Status {resp.status_code}: {resp.text[:200]}")
