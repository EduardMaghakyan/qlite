//go:build ignore

// semantic_realtest.go — Integration test for the semantic cache feature.
// Validates that semantically similar messages produce cache hits via Qdrant,
// while exact matches, unrelated messages, and temperature>0 behave correctly.
//
// Prerequisites:
//   docker run -d -p 6333:6333 qdrant/qdrant
//   QLITE_CONFIG=config/config.yaml OPENAI_API_KEY=... go run ./cmd/proxy
//
// Usage:
//   OPENAI_API_KEY=sk-... go run loadtest/semantic_realtest.go
//
// Env vars:
//   OPENAI_API_KEY   — required
//   QLITE_PROXY_URL  — default http://localhost:8080
//   QLITE_RUNS       — default 3 (repetitions per measurement)
//   QLITE_TEST_MODEL — default gpt-4o-mini

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var (
	proxyURL = env("QLITE_PROXY_URL", "http://localhost:8080")
	apiKey   = func() string {
		k := os.Getenv("OPENAI_API_KEY")
		if k == "" {
			fail("OPENAI_API_KEY is required")
		}
		return k
	}()
	model = env("QLITE_TEST_MODEL", "gpt-4o-mini")
	runs  = func() int {
		n, err := strconv.Atoi(env("QLITE_RUNS", "3"))
		if err != nil || n < 1 {
			return 3
		}
		return n
	}()
)

// ---------------------------------------------------------------------------
// Test message groups
// ---------------------------------------------------------------------------

type messageGroup struct {
	name     string
	original string
	similar  []string
}

var groups = []messageGroup{
	{
		name:     "Greeting",
		original: "Say hello in exactly three words",
		similar: []string{
			"Say hello in precisely three words",
			"Say hello using exactly three words",
		},
	},
	{
		name:     "Geography",
		original: "What is the capital of France?",
		similar:  []string{"What is the capital city of France?"},
	},
}

const unrelatedMessage = "Explain the theory of general relativity in one sentence"

// ---------------------------------------------------------------------------
// OpenAI-format payloads
// ---------------------------------------------------------------------------

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens"`
	Stream      bool      `json:"stream,omitempty"`
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

var client = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 10,
	},
}

type result struct {
	Latency     time.Duration
	XCache      string
	Cost        string
	CostSaved   string
	TokensSaved string
	Provider    string
	StatusCode  int
}

func doNonStreamProxy(url, authKey, mdl, content string) (result, error) {
	payload := chatRequest{
		Model:     mdl,
		Messages:  []message{{Role: "user", Content: content}},
		MaxTokens: 10,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authKey)

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return result{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	return result{
		Latency:     latency,
		XCache:      resp.Header.Get("X-Cache"),
		Cost:        resp.Header.Get("X-Request-Cost"),
		CostSaved:   resp.Header.Get("X-Cost-Saved"),
		TokensSaved: resp.Header.Get("X-Tokens-Saved"),
		Provider:    resp.Header.Get("X-Provider"),
		StatusCode:  resp.StatusCode,
	}, nil
}

func doNonStreamProxyWithTemp(url, authKey, mdl, content string, temp float64) (result, error) {
	payload := chatRequest{
		Model:       mdl,
		Messages:    []message{{Role: "user", Content: content}},
		Temperature: &temp,
		MaxTokens:   10,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authKey)

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return result{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	return result{
		Latency:     latency,
		XCache:      resp.Header.Get("X-Cache"),
		Cost:        resp.Header.Get("X-Request-Cost"),
		TokensSaved: resp.Header.Get("X-Tokens-Saved"),
		Provider:    resp.Header.Get("X-Provider"),
		StatusCode:  resp.StatusCode,
	}, nil
}

func doStreamTTFBProxy(url, authKey, mdl, content string) (result, error) {
	payload := chatRequest{
		Model:     mdl,
		Messages:  []message{{Role: "user", Content: content}},
		MaxTokens: 10,
		Stream:    true,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authKey)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return result{}, fmt.Errorf("request failed: %w", err)
	}

	scanner := bufio.NewScanner(resp.Body)
	var ttfb time.Duration
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			ttfb = time.Since(start)
			break
		}
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if ttfb == 0 {
		return result{}, fmt.Errorf("no SSE data received")
	}

	return result{
		Latency:  ttfb,
		XCache:   resp.Header.Get("X-Cache"),
		Provider: resp.Header.Get("X-Provider"),
	}, nil
}

// ---------------------------------------------------------------------------
// Measurement helpers
// ---------------------------------------------------------------------------

func median(durations []time.Duration) time.Duration {
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[len(durations)/2]
}

func measureProxy(label, url, authKey, mdl, content string, n int) (result, error) {
	latencies := make([]time.Duration, 0, n)
	var last result
	for i := 0; i < n; i++ {
		r, err := doNonStreamProxy(url, authKey, mdl, content)
		if err != nil {
			return result{}, fmt.Errorf("%s run %d: %w", label, i+1, err)
		}
		if r.StatusCode != 200 {
			return result{}, fmt.Errorf("%s run %d: got status %d", label, i+1, r.StatusCode)
		}
		latencies = append(latencies, r.Latency)
		last = r
	}
	last.Latency = median(latencies)
	return last, nil
}

func measureStreamTTFB(label, url, authKey, mdl, content string, n int) (result, error) {
	latencies := make([]time.Duration, 0, n)
	var last result
	for i := 0; i < n; i++ {
		r, err := doStreamTTFBProxy(url, authKey, mdl, content)
		if err != nil {
			return result{}, fmt.Errorf("%s run %d: %w", label, i+1, err)
		}
		latencies = append(latencies, r.Latency)
		last = r
	}
	last.Latency = median(latencies)
	return last, nil
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

var passed = true

func assertProvider(step string, r result, want string) {
	if r.Provider != want {
		fmt.Printf("  FAIL: %s — expected X-Provider=%s, got %q\n", step, want, r.Provider)
		passed = false
	}
}

func assertCache(step string, r result, want string) {
	if r.XCache != want {
		fmt.Printf("  FAIL: %s — expected X-Cache=%s, got %q\n", step, want, r.XCache)
		passed = false
	}
}

func waitForAsyncStore() {
	fmt.Println("  Waiting for Qdrant store...")
	time.Sleep(2 * time.Second)
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func clearCaches() {
	req, _ := http.NewRequest("POST", proxyURL+"/admin/cache/clear", nil)
	resp, err := client.Do(req)
	if err != nil {
		fail("cache clear request failed (is the proxy running?): %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		fail("cache clear returned status %d (proxy may need updating)", resp.StatusCode)
	}
}

func main() {
	fmt.Println("=== qlite Semantic Cache Integration Test ===")
	fmt.Printf("Provider: openai | Model: %s | Proxy: %s\n\n", model, proxyURL)

	fmt.Print("Clearing caches... ")
	clearCaches()
	fmt.Println("done")

	// Step 1: Warmup — prime connection pool.
	fmt.Print("Warming up... ")
	if _, err := doNonStreamProxy(proxyURL, apiKey, model, "warmup"); err != nil {
		fail("warm-up failed: %v", err)
	}
	fmt.Println("done")

	grp := groups[0] // Greeting group

	// --- Seeding ---
	fmt.Println("\n--- Seeding ---")

	seed, err := measureProxy("Seed", proxyURL, apiKey, model, grp.original, 1)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("  Original request:    %4dms  X-Cache=%s  X-Provider=%s\n",
		seed.Latency.Milliseconds(), seed.XCache, seed.Provider)
	assertCache("Seed", seed, "MISS")

	waitForAsyncStore()

	// --- Exact Cache ---
	fmt.Println("\n--- Exact Cache ---")

	exact, err := measureProxy("Exact", proxyURL, apiKey, model, grp.original, runs)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("  Identical request:   %4dms  X-Cache=%s  X-Provider=%s",
		exact.Latency.Milliseconds(), exact.XCache, exact.Provider)
	assertCache("Exact hit", exact, "HIT")
	assertProvider("Exact hit", exact, "cache")
	fmt.Println("  ✓")

	// --- Semantic Cache ---
	fmt.Println("\n--- Semantic Cache ---")

	sem1, err := measureProxy("Semantic 1", proxyURL, apiKey, model, grp.similar[0], 1)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("  Similar variant:     %4dms  X-Cache=%s  X-Provider=%s  cost=%s",
		sem1.Latency.Milliseconds(), sem1.XCache, sem1.Provider, sem1.Cost)
	assertCache("Semantic hit 1", sem1, "HIT")
	assertProvider("Semantic hit 1", sem1, "semantic_cache")
	fmt.Println("  ✓")

	if len(grp.similar) > 1 {
		sem2, err := measureProxy("Semantic 2", proxyURL, apiKey, model, grp.similar[1], 1)
		if err != nil {
			fail("%v", err)
		}
		fmt.Printf("  Another variant:     %4dms  X-Cache=%s  X-Provider=%s",
			sem2.Latency.Milliseconds(), sem2.XCache, sem2.Provider)
		assertCache("Semantic hit 2", sem2, "HIT")
		assertProvider("Semantic hit 2", sem2, "semantic_cache")
		fmt.Println("  ✓")
	}

	unrelated, err := measureProxy("Unrelated", proxyURL, apiKey, model, unrelatedMessage, 1)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("  Unrelated (miss):    %4dms  X-Cache=%s  X-Provider=%s",
		unrelated.Latency.Milliseconds(), unrelated.XCache, unrelated.Provider)
	assertCache("Unrelated miss", unrelated, "MISS")
	fmt.Println("  ✓")

	// --- Streaming ---
	fmt.Println("\n--- Streaming ---")

	streamSem, err := measureStreamTTFB("Stream semantic", proxyURL, apiKey, model, grp.similar[0], 1)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("  Stream semantic hit: TTFB=%dms  X-Cache=%s  X-Provider=%s",
		streamSem.Latency.Milliseconds(), streamSem.XCache, streamSem.Provider)
	assertCache("Stream semantic hit", streamSem, "HIT")
	assertProvider("Stream semantic hit", streamSem, "semantic_cache")
	fmt.Println("  ✓")

	// --- Edge Cases ---
	fmt.Println("\n--- Edge Cases ---")

	tempBypass, err := doNonStreamProxyWithTemp(proxyURL, apiKey, model, grp.similar[0], 0.7)
	if err != nil {
		fail("temperature bypass: %v", err)
	}
	if tempBypass.StatusCode != 200 {
		fail("temperature bypass: got status %d", tempBypass.StatusCode)
	}
	fmt.Printf("  Temperature>0 bypass:%4dms  X-Provider=%s",
		tempBypass.Latency.Milliseconds(), tempBypass.Provider)
	// With temp>0 the semantic cache is bypassed; exact cache also skips temp>0.
	// Provider should be the upstream, not any cache.
	if tempBypass.Provider == "cache" || tempBypass.Provider == "semantic_cache" {
		fmt.Printf("\n  FAIL: Temperature>0 bypass — expected upstream provider, got %q\n", tempBypass.Provider)
		passed = false
	}
	fmt.Println("  ✓")

	// --- Second Group (Geography) ---
	fmt.Println("\n--- Second Group ---")

	grp2 := groups[1]

	seed2, err := measureProxy("Seed geo", proxyURL, apiKey, model, grp2.original, 1)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("  Seed geography:      %4dms  X-Cache=%s\n",
		seed2.Latency.Milliseconds(), seed2.XCache)
	assertCache("Seed geography", seed2, "MISS")

	waitForAsyncStore()

	semGeo, err := measureProxy("Semantic geo", proxyURL, apiKey, model, grp2.similar[0], 1)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("  Similar geography:   %4dms  X-Cache=%s  X-Provider=%s",
		semGeo.Latency.Milliseconds(), semGeo.XCache, semGeo.Provider)
	assertCache("Semantic geo hit", semGeo, "HIT")
	assertProvider("Semantic geo hit", semGeo, "semantic_cache")
	fmt.Println("  ✓")

	// --- Cost Summary ---
	fmt.Println("\n--- Cost Summary ---")
	seedCost := parseFloat(seed.Cost)
	seed2Cost := parseFloat(seed2.Cost)
	totalAPICost := seedCost + seed2Cost
	sem1Saved := parseFloat(sem1.CostSaved)
	semGeoSaved := parseFloat(semGeo.CostSaved)
	totalSaved := sem1Saved + semGeoSaved

	fmt.Printf("  Seed API cost:       $%.8f\n", seedCost)
	fmt.Printf("  Seed 2 API cost:     $%.8f\n", seed2Cost)
	fmt.Printf("  Total API cost:      $%.8f\n", totalAPICost)
	fmt.Printf("  Semantic hit 1 saved:$%.8f\n", sem1Saved)
	fmt.Printf("  Semantic geo saved:  $%.8f\n", semGeoSaved)
	fmt.Printf("  Total saved:         $%.8f\n", totalSaved)

	// --- Result ---
	fmt.Println()
	if passed {
		fmt.Println("PASS (all assertions passed)")
	} else {
		fmt.Println("FAIL (some assertions failed)")
		os.Exit(1)
	}
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	os.Exit(1)
}
