//go:build ignore

// realtest.go — Integration test against a live proxy + upstream LLM APIs.
// Measures proxy overhead and cache savings.
//
// Usage:
//   OPENAI_API_KEY=sk-... go run loadtest/realtest.go
//   ANTHROPIC_API_KEY=sk-... QLITE_TEST_PROVIDER=anthropic go run loadtest/realtest.go
//
// Env vars:
//   QLITE_TEST_PROVIDER — "openai" (default) or "anthropic"
//   OPENAI_API_KEY      — required when provider=openai
//   ANTHROPIC_API_KEY   — required when provider=anthropic
//   QLITE_TEST_MODEL    — override model (default: gpt-4o-mini / claude-haiku-4-5)
//   QLITE_PROXY_URL     — default http://localhost:8080
//   QLITE_RUNS          — default 3 (repetitions per measurement)

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
	provider = env("QLITE_TEST_PROVIDER", "openai")
	proxyURL = env("QLITE_PROXY_URL", "http://localhost:8080")
	runs     = func() int {
		n, err := strconv.Atoi(env("QLITE_RUNS", "3"))
		if err != nil || n < 1 {
			return 3
		}
		return n
	}()
)

// providerConfig holds provider-specific settings.
type providerConfig struct {
	name      string
	apiKey    string
	model     string
	directURL string // base URL for direct upstream calls
}

func getProviderConfig() providerConfig {
	switch provider {
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			fail("ANTHROPIC_API_KEY is required when provider=anthropic")
		}
		return providerConfig{
			name:      "anthropic",
			apiKey:    key,
			model:     env("QLITE_TEST_MODEL", "claude-haiku-4-5"),
			directURL: "https://api.anthropic.com",
		}
	default: // openai
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			fail("OPENAI_API_KEY is required when provider=openai")
		}
		return providerConfig{
			name:      "openai",
			apiKey:    key,
			model:     env("QLITE_TEST_MODEL", "gpt-4o-mini"),
			directURL: "https://api.openai.com",
		}
	}
}

// ---------------------------------------------------------------------------
// OpenAI-format payloads (used for proxy requests, and direct OpenAI calls)
// ---------------------------------------------------------------------------

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	Stream      bool      `json:"stream,omitempty"`
}

// ---------------------------------------------------------------------------
// Anthropic-native payloads (used for direct Anthropic calls only)
// ---------------------------------------------------------------------------

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string         `json:"model"`
	Messages    []anthropicMsg `json:"messages"`
	MaxTokens   int            `json:"max_tokens"`
	Temperature float64        `json:"temperature"`
	Stream      bool           `json:"stream,omitempty"`
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
	TokensSaved string
	Provider    string
	StatusCode  int
}

// doNonStreamProxy sends an OpenAI-format request (used for all proxy calls).
func doNonStreamProxy(url, authKey string, model string) (result, error) {
	payload := openAIChatRequest{
		Model:       model,
		Messages:    []message{{Role: "user", Content: "Say hello in exactly 3 words"}},
		Temperature: 0,
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

// doNonStreamDirectOpenAI sends a direct request to OpenAI.
func doNonStreamDirectOpenAI(baseURL, authKey, model string) (result, error) {
	payload := openAIChatRequest{
		Model:       model,
		Messages:    []message{{Role: "user", Content: "Say hello in exactly 3 words"}},
		Temperature: 0,
		MaxTokens:   10,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", baseURL+"/v1/chat/completions", bytes.NewReader(body))
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
		Latency:    latency,
		StatusCode: resp.StatusCode,
	}, nil
}

// doNonStreamDirectAnthropic sends a direct request to Anthropic's Messages API.
func doNonStreamDirectAnthropic(baseURL, authKey, model string) (result, error) {
	payload := anthropicRequest{
		Model:       model,
		Messages:    []anthropicMsg{{Role: "user", Content: "Say hello in exactly 3 words"}},
		MaxTokens:   10,
		Temperature: 0,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", baseURL+"/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", authKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return result{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	return result{
		Latency:    latency,
		StatusCode: resp.StatusCode,
	}, nil
}

// doStreamTTFBProxy sends a streaming OpenAI-format request through the proxy.
func doStreamTTFBProxy(url, authKey, model string) (result, error) {
	payload := openAIChatRequest{
		Model:       model,
		Messages:    []message{{Role: "user", Content: "Say hello in exactly 3 words"}},
		Temperature: 0,
		MaxTokens:   10,
		Stream:      true,
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
		Latency:    ttfb,
		XCache:     resp.Header.Get("X-Cache"),
		StatusCode: resp.StatusCode,
	}, nil
}

// ---------------------------------------------------------------------------
// Measurement helpers
// ---------------------------------------------------------------------------

func median(durations []time.Duration) time.Duration {
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[len(durations)/2]
}

type measurement struct {
	Median      time.Duration
	XCache      string
	Cost        string
	TokensSaved string
	Provider    string
}

// directFunc is a function that makes a direct (non-proxy) request.
type directFunc func(baseURL, authKey, model string) (result, error)

func measureDirect(label string, fn directFunc, baseURL, authKey, model string, n int) (measurement, error) {
	latencies := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		r, err := fn(baseURL, authKey, model)
		if err != nil {
			return measurement{}, fmt.Errorf("%s run %d: %w", label, i+1, err)
		}
		if r.StatusCode != 200 {
			return measurement{}, fmt.Errorf("%s run %d: got status %d", label, i+1, r.StatusCode)
		}
		latencies = append(latencies, r.Latency)
	}
	return measurement{Median: median(latencies)}, nil
}

func measureProxy(label, url, authKey, model string, n int) (measurement, error) {
	latencies := make([]time.Duration, 0, n)
	var last result
	for i := 0; i < n; i++ {
		r, err := doNonStreamProxy(url, authKey, model)
		if err != nil {
			return measurement{}, fmt.Errorf("%s run %d: %w", label, i+1, err)
		}
		if r.StatusCode != 200 {
			return measurement{}, fmt.Errorf("%s run %d: got status %d", label, i+1, r.StatusCode)
		}
		latencies = append(latencies, r.Latency)
		last = r
	}
	return measurement{
		Median:      median(latencies),
		XCache:      last.XCache,
		Cost:        last.Cost,
		TokensSaved: last.TokensSaved,
		Provider:    last.Provider,
	}, nil
}

func measureStreamTTFB(label, url, authKey, model string, n int) (measurement, error) {
	latencies := make([]time.Duration, 0, n)
	var last result
	for i := 0; i < n; i++ {
		r, err := doStreamTTFBProxy(url, authKey, model)
		if err != nil {
			return measurement{}, fmt.Errorf("%s run %d: %w", label, i+1, err)
		}
		if r.StatusCode != 200 {
			return measurement{}, fmt.Errorf("%s run %d: got status %d", label, i+1, r.StatusCode)
		}
		latencies = append(latencies, r.Latency)
		last = r
	}
	return measurement{
		Median: median(latencies),
		XCache: last.XCache,
	}, nil
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	cfg := getProviderConfig()

	fmt.Println("=== qlite Real Integration Test ===")
	fmt.Printf("Provider: %s | Model: %s | Proxy: %s | Runs: %d\n\n", cfg.name, cfg.model, proxyURL, runs)

	// Pick the right direct-call function based on provider.
	var directFn directFunc
	switch cfg.name {
	case "anthropic":
		directFn = doNonStreamDirectAnthropic
	default:
		directFn = doNonStreamDirectOpenAI
	}

	// Step 1: Warm-up — prime connection pool and tiktoken cache.
	fmt.Print("Warming up... ")
	if _, err := doNonStreamProxy(proxyURL, cfg.apiKey, cfg.model); err != nil {
		fail("warm-up failed: %v", err)
	}
	fmt.Println("done")

	// --- Non-Streaming ---
	fmt.Println("\n--- Non-Streaming ---")

	// Step 2: Direct to upstream.
	direct, err := measureDirect("Direct", directFn, cfg.directURL, cfg.apiKey, cfg.model, runs)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("  Direct to %s:%s%4dms (median of %d)\n",
		cfg.name, pad(cfg.name, 12), direct.Median.Milliseconds(), runs)

	// Step 3: Through proxy — expect MISS.
	proxyMiss, err := measureProxy("Proxy MISS", proxyURL, cfg.apiKey, cfg.model, runs)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("  Proxy (MISS):        %4dms (median of %d)  X-Cache=%s  cost=%s\n",
		proxyMiss.Median.Milliseconds(), runs, proxyMiss.XCache, proxyMiss.Cost)

	// Step 4: Through proxy — expect HIT (same payload, cache should have it now).
	proxyHit, err := measureProxy("Proxy HIT", proxyURL, cfg.apiKey, cfg.model, runs)
	if err != nil {
		fail("%v", err)
	}
	if proxyHit.XCache != "HIT" {
		fail("expected X-Cache=HIT, got %q", proxyHit.XCache)
	}
	fmt.Printf("  Proxy (HIT):         %4dms (median of %d)  X-Cache=%s  cost=%s  tokens-saved=%s\n",
		proxyHit.Median.Milliseconds(), runs, proxyHit.XCache, proxyHit.Cost, proxyHit.TokensSaved)

	overhead := proxyMiss.Median - direct.Median
	if overhead < 0 {
		overhead = 0
	}
	fmt.Printf("  Overhead:            %4dms\n", overhead.Milliseconds())

	if proxyHit.Median > 0 {
		speedup := float64(direct.Median) / float64(proxyHit.Median)
		fmt.Printf("  Cache speedup:       %4.0fx\n", speedup)
	}

	// --- Streaming ---
	fmt.Println("\n--- Streaming ---")

	// Step 5: Stream through proxy — first request populates cache.
	streamMiss, err := measureStreamTTFB("Stream MISS", proxyURL, cfg.apiKey, cfg.model, runs)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("  Proxy TTFB (first):  %4dms (median of %d)  X-Cache=%s\n",
		streamMiss.Median.Milliseconds(), runs, streamMiss.XCache)

	// Step 6: Stream through proxy — expect HIT.
	streamHit, err := measureStreamTTFB("Stream HIT", proxyURL, cfg.apiKey, cfg.model, runs)
	if err != nil {
		fail("%v", err)
	}
	if streamHit.XCache != "HIT" {
		fail("expected streaming X-Cache=HIT, got %q", streamHit.XCache)
	}
	fmt.Printf("  Proxy TTFB (HIT):    %4dms (median of %d)  X-Cache=%s\n",
		streamHit.Median.Milliseconds(), runs, streamHit.XCache)

	if streamHit.Median > 0 {
		speedup := float64(streamMiss.Median) / float64(streamHit.Median)
		fmt.Printf("  Cache TTFB speedup:  %4.0fx\n", speedup)
	}

	fmt.Println("\nPASS")
}

// pad returns spaces to right-align after a label to a given width.
func pad(s string, width int) string {
	n := width - len(s)
	if n <= 0 {
		return " "
	}
	return strings.Repeat(" ", n)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	os.Exit(1)
}
