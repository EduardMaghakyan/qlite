package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/sse"
)

var bufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

var (
	dataPrefix = []byte("data: ")
	doneMarker = []byte("[DONE]")
	usageKey   = []byte(`"usage"`)
)

// OpenAICompat is a provider that speaks the OpenAI-compatible API.
type OpenAICompat struct {
	name    string
	baseURL string
	apiKey  string
	models  []string
	client  *http.Client
}

// NewOpenAICompat creates a new OpenAI-compatible provider.
func NewOpenAICompat(name, baseURL, apiKey string, models []string) *OpenAICompat {
	transport := &http.Transport{
		DisableCompression:  true,
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 1000,
		IdleConnTimeout:     90 * time.Second,
		WriteBufferSize:     32 << 10,
		ReadBufferSize:      32 << 10,
		ForceAttemptHTTP2:   true,
	}
	return &OpenAICompat{
		name:    name,
		baseURL: baseURL,
		apiKey:  apiKey,
		models:  models,
		client:  &http.Client{Transport: transport},
	}
}

func (o *OpenAICompat) Name() string    { return o.name }
func (o *OpenAICompat) Models() []string { return o.models }

// Chat sends a non-streaming chat completion request.
func (o *OpenAICompat) Chat(ctx context.Context, req *model.ChatRequest) (*model.ChatResponse, error) {
	// Ensure stream is false.
	req.Stream = false
	req.StreamOptions = nil

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := json.NewEncoder(buf).Encode(req); err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	o.setHeaders(httpReq)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("upstream error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var chatResp model.ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &chatResp, nil
}

// ChatStream sends a streaming chat completion request and relays SSE chunks.
func (o *OpenAICompat) ChatStream(ctx context.Context, req *model.ChatRequest, sw sse.Writer) (*model.Usage, error) {
	// Enable streaming with usage.
	req.Stream = true
	req.StreamOptions = &model.StreamOptions{IncludeUsage: true}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := json.NewEncoder(buf).Encode(req); err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	o.setHeaders(httpReq)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("upstream error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var usage *model.Usage
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, dataPrefix) {
			continue
		}
		data := line[len(dataPrefix):]
		if bytes.Equal(data, doneMarker) {
			if err := sw.Done(); err != nil {
				return usage, fmt.Errorf("writing done: %w", err)
			}
			break
		}

		// Only parse chunks that contain a usage field (typically the final chunk).
		if bytes.Contains(data, usageKey) {
			var chunk model.ChatStreamChunk
			if err := json.Unmarshal(data, &chunk); err == nil && chunk.Usage != nil {
				usage = chunk.Usage
			}
		}

		// Forward the raw chunk immediately.
		if err := sw.WriteEvent(data); err != nil {
			return usage, fmt.Errorf("writing event: %w", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("reading stream: %w", err)
	}

	return usage, nil
}

func (o *OpenAICompat) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
}
