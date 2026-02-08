"""
Semantic cache load test for qlite proxy.

Exercises exact cache, semantic cache, and cache misses at configurable rates.

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
# Message pools
# ---------------------------------------------------------------------------

# Semantic pool: (seed_message, [variant_messages])
# Seeds are sent during warmup to populate both exact and semantic caches.
# Variants are sent during load to trigger semantic cache hits.
SEMANTIC_POOL = [
    (
        "Say hello in exactly 3 words",
        [
            "Greet me using three words",
            "Give me a 3-word greeting",
            "Say hi in 3 words",
        ],
    ),
    (
        "What is the capital of France?",
        [
            "Tell me the capital city of France",
            "Which city is the capital of France?",
            "Name the French capital",
        ],
    ),
    (
        "Count from 1 to 5",
        [
            "List the numbers 1 through 5",
            "Give me the numbers one to five",
            "Enumerate 1 to 5",
        ],
    ),
    (
        "What color is the sky?",
        [
            "Tell me the color of the sky",
            "What colour is the sky on a clear day?",
            "Describe the sky's color",
        ],
    ),
    (
        "Name three fruits",
        [
            "List 3 fruits",
            "Give me three fruit names",
            "What are three common fruits?",
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
_stats_lock = threading.Lock()


def record_result(cache_header, provider_header):
    global _exact_hits, _semantic_hits, _misses
    with _stats_lock:
        if cache_header == "HIT" and provider_header == "semantic_cache":
            _semantic_hits += 1
        elif cache_header == "HIT" and provider_header == "cache":
            _exact_hits += 1
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
            # Cache miss — unique message.
            uid = next_miss_id()
            messages = [{"role": "user", "content": f"Unique miss message {uid}"}]
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
                record_result(cache_header, provider_header)
                body = resp.json()
                if "choices" not in body or len(body["choices"]) == 0:
                    resp.failure("No choices in response")
                else:
                    resp.success()
            else:
                record_result("MISS", "")
                resp.failure(f"Status {resp.status_code}: {resp.text[:200]}")
