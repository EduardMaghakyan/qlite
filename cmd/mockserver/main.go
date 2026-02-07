package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
)

var (
	port    int
	latency time.Duration
)

func main() {
	flag.IntVar(&port, "port", 9999, "listen port")
	flag.DurationVar(&latency, "latency", 50*time.Millisecond, "simulated latency (per-chunk for streaming)")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", handleChat)
	mux.HandleFunc("GET /health", handleHealth)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("mock-openai listening on %s (latency=%v)", addr, latency)
	log.Fatal(http.ListenAndServe(addr, mux))
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

	resp := model.ChatResponse{
		ID:      "mock-completion-001",
		Object:  "chat.completion",
		Created: created,
		Model:   reqModel,
		Choices: []model.Choice{
			{
				Index:        0,
				Message:      model.Message{Role: "assistant", Content: "This is a mock response from the qlite mock server."},
				FinishReason: "stop",
			},
		},
		Usage: model.Usage{
			PromptTokens:     10,
			CompletionTokens: 12,
			TotalTokens:      22,
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

	chunks := []model.ChatStreamChunk{
		{
			ID: "mock-completion-001", Object: "chat.completion.chunk", Created: created, Model: reqModel,
			Choices: []model.StreamChoice{{Index: 0, Delta: model.Delta{Role: "assistant"}}},
		},
		{
			ID: "mock-completion-001", Object: "chat.completion.chunk", Created: created, Model: reqModel,
			Choices: []model.StreamChoice{{Index: 0, Delta: model.Delta{Content: "This is a mock response from the qlite mock server."}}},
		},
		{
			ID: "mock-completion-001", Object: "chat.completion.chunk", Created: created, Model: reqModel,
			Choices: []model.StreamChoice{{Index: 0, Delta: model.Delta{}, FinishReason: "stop"}},
			Usage:   &model.Usage{PromptTokens: 10, CompletionTokens: 12, TotalTokens: 22},
		},
	}

	for _, chunk := range chunks {
		time.Sleep(latency)
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		rc.Flush()
	}

	fmt.Fprint(w, "data: [DONE]\n\n")
	rc.Flush()
}
