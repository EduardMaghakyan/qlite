package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/cache"
	"github.com/eduardmaghakyan/qlite/internal/config"
	"github.com/eduardmaghakyan/qlite/internal/embedding"
	"github.com/eduardmaghakyan/qlite/internal/pipeline"
	"github.com/eduardmaghakyan/qlite/internal/provider"
	"github.com/eduardmaghakyan/qlite/internal/qdrant"
	"github.com/eduardmaghakyan/qlite/internal/server"
	"github.com/eduardmaghakyan/qlite/internal/tokenizer"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if os.Getenv("QLITE_PPROF") == "1" {
		go func() {
			logger.Info("pprof enabled on :6060")
			if err := http.ListenAndServe(":6060", nil); err != nil {
				logger.Error("pprof server error", "error", err)
			}
		}()
	}

	configPath := "config/config.yaml"
	if p := os.Getenv("QLITE_CONFIG"); p != "" {
		configPath = p
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	counter := tokenizer.NewCounter()
	registry := provider.NewRegistry()

	for _, pc := range cfg.Providers {
		switch pc.Type {
		case "openai":
			p := provider.NewOpenAICompat(pc.Name, pc.BaseURL, pc.APIKey, pc.Models)
			registry.Register(p)
			logger.Info("registered provider", "name", pc.Name, "models", pc.Models)
		case "anthropic":
			p := provider.NewAnthropic(pc.Name, pc.BaseURL, pc.APIKey, pc.Models)
			registry.Register(p)
			logger.Info("registered provider", "name", pc.Name, "models", pc.Models)
		case "google":
			p := provider.NewGoogle(pc.Name, pc.BaseURL, pc.APIKey, pc.Models)
			registry.Register(p)
			logger.Info("registered provider", "name", pc.Name, "models", pc.Models)
		default:
			logger.Warn("unknown provider type, skipping", "type", pc.Type, "name", pc.Name)
		}
	}
	registry.Freeze()

	var exactCache *cache.ExactCache
	if cfg.Cache.Exact.Enabled {
		exactCache = cache.New(cfg.Cache.Exact.TTL, cfg.Cache.Exact.MaxEntries)
		logger.Info("exact cache enabled", "ttl", cfg.Cache.Exact.TTL, "max_entries", cfg.Cache.Exact.MaxEntries)
	}

	dispatch := pipeline.NewDispatchStage(registry, counter)

	// Build the final stage: either SemanticDispatchStage (wrapping dispatch) or plain dispatch.
	var finalStage any = dispatch
	var qdrantClient *qdrant.Client
	if cfg.Cache.Semantic.Enabled {
		embClient := embedding.NewClient(
			cfg.Cache.Semantic.EmbeddingURL,
			cfg.Cache.Semantic.EmbeddingKey,
			cfg.Cache.Semantic.EmbeddingModel,
		)
		qdrantClient = qdrant.NewClient(
			cfg.Cache.Semantic.QdrantURL,
			cfg.Cache.Semantic.QdrantAPIKey,
			cfg.Cache.Semantic.QdrantCollection,
		)

		// Best-effort collection creation — warn on failure, don't abort.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := qdrantClient.EnsureCollection(ctx, 1536); err != nil {
			logger.Warn("failed to ensure qdrant collection, semantic cache disabled", "error", err)
			cancel()
		} else {
			cancel()
			sc := cache.NewSemanticCache(embClient, qdrantClient, cfg.Cache.Semantic.Threshold)
			finalStage = pipeline.NewSemanticDispatchStage(sc, dispatch, logger)
			logger.Info("semantic cache enabled",
				"threshold", cfg.Cache.Semantic.Threshold,
				"qdrant_url", cfg.Cache.Semantic.QdrantURL,
				"embedding_model", cfg.Cache.Semantic.EmbeddingModel,
			)
		}
	}

	var stages []any
	if exactCache != nil {
		stages = append(stages, pipeline.NewCacheStage(exactCache, true))
	}
	stages = append(stages, finalStage)

	pipe, err := pipeline.New(stages...)
	if err != nil {
		logger.Error("failed to create pipeline", "error", err)
		os.Exit(1)
	}

	handler := server.NewHandler(pipe, counter, logger, exactCache)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	mux.HandleFunc("POST /admin/cache/clear", func(w http.ResponseWriter, r *http.Request) {
		if exactCache != nil {
			exactCache.Clear()
		}
		if qdrantClient != nil {
			ctx := r.Context()
			if err := qdrantClient.DeleteCollection(ctx); err != nil {
				logger.Error("failed to delete qdrant collection", "error", err)
				http.Error(w, "failed to delete qdrant collection", http.StatusInternalServerError)
				return
			}
			if err := qdrantClient.EnsureCollection(ctx, 1536); err != nil {
				logger.Error("failed to recreate qdrant collection", "error", err)
				http.Error(w, "failed to recreate qdrant collection", http.StatusInternalServerError)
				return
			}
		}
		logger.Info("cache cleared via admin endpoint")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	wrapped := server.Chain(mux,
		server.RequestID,
		server.Logger(logger),
		server.Recovery(logger),
		server.CORS,
	)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           wrapped,
		ReadTimeout:       cfg.Server.ReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.Server.WriteTimeout,
	}

	go func() {
		logger.Info("starting qlite proxy", "port", cfg.Server.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("server forced to shutdown", "error", err)
	}
	logger.Info("server stopped")
}
