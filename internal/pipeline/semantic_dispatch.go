package pipeline

import (
	"context"
	"sync"

	"github.com/eduardmaghakyan/qlite/internal/cache"
	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/sse"
)

// SemanticDispatchStage races a semantic cache lookup against provider dispatch.
// If the semantic cache returns first with a hit, the provider call is cancelled.
// If the provider returns first (or semantic misses), the provider result is used
// and the response is stored in Qdrant asynchronously.
type SemanticDispatchStage struct {
	semantic *cache.SemanticCache
	dispatch *DispatchStage
}

// NewSemanticDispatchStage creates a stage that races semantic cache against dispatch.
func NewSemanticDispatchStage(semantic *cache.SemanticCache, dispatch *DispatchStage) *SemanticDispatchStage {
	return &SemanticDispatchStage{
		semantic: semantic,
		dispatch: dispatch,
	}
}

func (s *SemanticDispatchStage) Name() string { return "semantic_dispatch" }

type raceResult struct {
	resp *model.ProxyResponse
	emb  []float32
	err  error
	from string
}

// Process handles non-streaming requests with parallel race.
func (s *SemanticDispatchStage) Process(ctx context.Context, req *model.ProxyRequest) (*model.ProxyResponse, error) {
	if s.shouldSkip(req) {
		return s.dispatch.Process(ctx, req)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan raceResult, 2)

	// Semantic path
	go func() {
		resp, emb, err := s.semantic.Lookup(ctx, &req.ChatRequest)
		if resp != nil {
			ch <- raceResult{
				resp: &model.ProxyResponse{
					ChatResponse: resp,
					OutputTokens: resp.Usage.CompletionTokens,
					Cost:         0,
					CacheStatus:  "HIT",
					ProviderName: "semantic_cache",
				},
				emb:  emb,
				from: "semantic",
			}
		} else {
			ch <- raceResult{emb: emb, err: err, from: "semantic"}
		}
	}()

	// Dispatch path
	go func() {
		resp, err := s.dispatch.Process(ctx, req)
		ch <- raceResult{resp: resp, err: err, from: "dispatch"}
	}()

	var dispatchResult *raceResult
	var semanticEmb []float32

	for i := 0; i < 2; i++ {
		r := <-ch
		switch r.from {
		case "semantic":
			if r.resp != nil {
				// Semantic cache hit — cancel dispatch and return.
				cancel()
				return r.resp, nil
			}
			semanticEmb = r.emb
		case "dispatch":
			rCopy := r
			dispatchResult = &rCopy
		}
	}

	if dispatchResult == nil {
		// Should not happen — dispatch always sends a result.
		return s.dispatch.Process(ctx, req)
	}

	if dispatchResult.err != nil {
		return nil, dispatchResult.err
	}

	// Async store: fire-and-forget with background context.
	if dispatchResult.resp != nil && dispatchResult.resp.ChatResponse != nil {
		resp := dispatchResult.resp.ChatResponse
		chatReq := req.ChatRequest
		emb := semanticEmb
		go s.semantic.Store(context.Background(), &chatReq, resp, emb)
	}

	return dispatchResult.resp, nil
}

// ProcessStream handles streaming requests with parallel race.
// The semantic lookup runs concurrently with provider dispatch. Both goroutines
// race to produce a result. A gatedWriter ensures only one path writes SSE events.
func (s *SemanticDispatchStage) ProcessStream(ctx context.Context, req *model.ProxyRequest, sw sse.Writer) (*model.ProxyResponse, error) {
	if s.shouldSkip(req) {
		return s.dispatch.ProcessStream(ctx, req, sw)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type semanticResult struct {
		resp *model.ChatResponse
		emb  []float32
	}

	semanticCh := make(chan semanticResult, 1)

	// Semantic path — runs in parallel with dispatch.
	go func() {
		resp, emb, _ := s.semantic.Lookup(ctx, &req.ChatRequest)
		semanticCh <- semanticResult{resp: resp, emb: emb}
	}()

	// Create a gated writer: dispatch writes go through this, but if semantic
	// wins first, we block dispatch from writing and replay the cached response.
	gw := &gatedWriter{inner: sw}

	type dispatchResult struct {
		resp *model.ProxyResponse
		err  error
	}
	dispatchCh := make(chan dispatchResult, 1)

	// Dispatch path — also runs in parallel.
	go func() {
		resp, err := s.dispatch.ProcessStream(ctx, req, gw)
		dispatchCh <- dispatchResult{resp: resp, err: err}
	}()

	// Wait for both results. Either can arrive first.
	var semRes *semanticResult
	var dispRes *dispatchResult

	for semRes == nil || dispRes == nil {
		select {
		case sr := <-semanticCh:
			semRes = &sr
			if sr.resp != nil && gw.claim() {
				// Semantic hit won the race — replay via SSE.
				cancel() // Cancel dispatch.
				sw.SetHeader("X-Cache", "HIT")
				sw.SetHeader("X-Provider", "semantic_cache")
				sseErr := sse.WriteResponseAsSSE(sw, sr.resp)
				// Drain dispatch channel to avoid goroutine leak.
				go func() { <-dispatchCh }()
				return &model.ProxyResponse{
					ChatResponse: sr.resp,
					OutputTokens: sr.resp.Usage.CompletionTokens,
					Cost:         0,
					CacheStatus:  "HIT",
					ProviderName: "semantic_cache",
				}, sseErr
			}
			// Semantic miss (or dispatch already started writing) — let dispatch continue.
			gw.release()
		case dr := <-dispatchCh:
			dispRes = &dr
		}
	}

	// If we get here, dispatch completed. Async-store if we have an embedding.
	if dispRes.err == nil && dispRes.resp != nil && dispRes.resp.ChatResponse != nil && semRes != nil {
		chatReq := req.ChatRequest
		resp := dispRes.resp.ChatResponse
		emb := semRes.emb
		go s.semantic.Store(context.Background(), &chatReq, resp, emb)
	}

	if dispRes.err != nil {
		return nil, dispRes.err
	}
	return dispRes.resp, nil
}

// shouldSkip returns true if this request should bypass semantic cache.
func (s *SemanticDispatchStage) shouldSkip(req *model.ProxyRequest) bool {
	return req.ChatRequest.Temperature != nil && *req.ChatRequest.Temperature > 0
}

// gatedWriter wraps an sse.Writer and blocks writes until released or claimed.
// This allows dispatch to start its HTTP request in parallel with semantic lookup,
// but prevents it from writing SSE events until we know the semantic result.
type gatedWriter struct {
	inner   sse.Writer
	mu      sync.Mutex
	gate    chan struct{} // closed when gate opens
	claimed bool         // true if semantic claimed (dispatch should discard writes)
	writing bool         // true once dispatch has started writing
}

// waitForGate blocks until the gate is opened (release or claim).
// Returns true if writes should proceed, false if they should be discarded.
func (g *gatedWriter) waitForGate() bool {
	g.mu.Lock()
	if g.gate == nil {
		g.gate = make(chan struct{})
	}
	gate := g.gate
	g.mu.Unlock()

	<-gate

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.claimed {
		return false
	}
	g.writing = true
	return true
}

// claim is called by the semantic winner. Returns true if dispatch hasn't started writing yet.
func (g *gatedWriter) claim() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.writing {
		return false
	}
	g.claimed = true
	if g.gate == nil {
		g.gate = make(chan struct{})
	}
	select {
	case <-g.gate:
	default:
		close(g.gate)
	}
	return true
}

// release opens the gate for dispatch writes to proceed.
func (g *gatedWriter) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gate == nil {
		g.gate = make(chan struct{})
	}
	select {
	case <-g.gate:
	default:
		close(g.gate)
	}
}

func (g *gatedWriter) SetHeader(key, value string) {
	g.inner.SetHeader(key, value)
}

func (g *gatedWriter) WriteEvent(data []byte) error {
	if !g.waitForGate() {
		return context.Canceled
	}
	return g.inner.WriteEvent(data)
}

func (g *gatedWriter) Done() error {
	if !g.waitForGate() {
		return context.Canceled
	}
	return g.inner.Done()
}
