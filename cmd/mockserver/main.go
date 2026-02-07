package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
)

var (
	port           int
	latency        time.Duration
	chunks         int
	responseTokens int
)

const loremCorpus = "Lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua Ut enim ad minim veniam quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur Excepteur sint occaecat cupidatat non proident sunt in culpa qui officia deserunt mollit anim id est laborum "

func main() {
	flag.IntVar(&port, "port", 9999, "listen port")
	flag.DurationVar(&latency, "latency", 50*time.Millisecond, "simulated latency (per-chunk for streaming)")
	flag.IntVar(&chunks, "chunks", 3, "number of SSE chunks for streaming (min 2: role + finish)")
	flag.IntVar(&responseTokens, "response-tokens", 10, "approximate content tokens (~5 chars each)")
	flag.Parse()

	if chunks < 2 {
		log.Fatal("-chunks must be >= 2 (need at least role delta + finish_reason)")
	}
	if responseTokens < 1 {
		log.Fatal("-response-tokens must be >= 1")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", handleChat)
	mux.HandleFunc("GET /health", handleHealth)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("mock-openai listening on %s (latency=%v, chunks=%d, response-tokens=%d)", addr, latency, chunks, responseTokens)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// generateContent returns a string of approximately tokenCount*5 characters
// using a repeating lorem ipsum corpus.
func generateContent(tokenCount int) string {
	targetLen := tokenCount * 5
	if targetLen <= len(loremCorpus) {
		return loremCorpus[:targetLen]
	}
	var b strings.Builder
	b.Grow(targetLen)
	for b.Len() < targetLen {
		remaining := targetLen - b.Len()
		if remaining >= len(loremCorpus) {
			b.WriteString(loremCorpus)
		} else {
			b.WriteString(loremCorpus[:remaining])
		}
	}
	return b.String()
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req model.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	reqModel := req.Model
	if reqModel == "" {
		reqModel = "gpt-4o-mini"
	}

	now := time.Now().Unix()

	if req.Stream {
		handleStream(w, reqModel, now)
	} else {
		handleNonStream(w, reqModel, now)
	}
}

func handleNonStream(w http.ResponseWriter, reqModel string, created int64) {
	time.Sleep(latency)

	content := generateContent(responseTokens)
	resp := model.ChatResponse{
		ID:      "mock-completion-001",
		Object:  "chat.completion",
		Created: created,
		Model:   reqModel,
		Choices: []model.Choice{
			{
				Index:        0,
				Message:      model.Message{Role: "assistant", Content: content},
				FinishReason: "stop",
			},
		},
		Usage: model.Usage{
			PromptTokens:     10,
			CompletionTokens: responseTokens,
			TotalTokens:      10 + responseTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleStream(w http.ResponseWriter, reqModel string, created int64) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	rc := http.NewResponseController(w)

	// Build chunks dynamically:
	//   chunk 0:         role delta ("assistant")
	//   chunks 1..N-2:   content deltas (evenly split)
	//   chunk N-1:        finish_reason + usage
	content := generateContent(responseTokens)
	contentChunks := chunks - 2 // middle chunks carry content
	if contentChunks < 1 {
		contentChunks = 1
	}

	// Split content evenly across middle chunks.
	chunkSize := len(content) / contentChunks
	if chunkSize < 1 {
		chunkSize = 1
	}

	sseChunks := make([]model.ChatStreamChunk, 0, chunks)

	// First chunk: role delta.
	sseChunks = append(sseChunks, model.ChatStreamChunk{
		ID: "mock-completion-001", Object: "chat.completion.chunk", Created: created, Model: reqModel,
		Choices: []model.StreamChoice{{Index: 0, Delta: model.Delta{Role: "assistant"}}},
	})

	// Middle chunks: content deltas.
	for i := 0; i < contentChunks; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if i == contentChunks-1 {
			end = len(content) // last middle chunk gets remainder
		}
		if start >= len(content) {
			break
		}
		sseChunks = append(sseChunks, model.ChatStreamChunk{
			ID: "mock-completion-001", Object: "chat.completion.chunk", Created: created, Model: reqModel,
			Choices: []model.StreamChoice{{Index: 0, Delta: model.Delta{Content: content[start:end]}}},
		})
	}

	// Final chunk: finish_reason + usage.
	sseChunks = append(sseChunks, model.ChatStreamChunk{
		ID: "mock-completion-001", Object: "chat.completion.chunk", Created: created, Model: reqModel,
		Choices: []model.StreamChoice{{Index: 0, Delta: model.Delta{}, FinishReason: "stop"}},
		Usage:   &model.Usage{PromptTokens: 10, CompletionTokens: responseTokens, TotalTokens: 10 + responseTokens},
	})

	for _, chunk := range sseChunks {
		time.Sleep(latency)
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		rc.Flush()
	}

	fmt.Fprint(w, "data: [DONE]\n\n")
	rc.Flush()
}
