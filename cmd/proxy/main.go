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

	"github.com/eduardmaghakyan/qlite/internal/config"
	"github.com/eduardmaghakyan/qlite/internal/pipeline"
	"github.com/eduardmaghakyan/qlite/internal/provider"
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
		default:
			logger.Warn("unknown provider type, skipping", "type", pc.Type, "name", pc.Name)
		}
	}
	registry.Freeze()

	dispatch := pipeline.NewDispatchStage(registry, counter)
	pipe, err := pipeline.New(dispatch)
	if err != nil {
		logger.Error("failed to create pipeline", "error", err)
		os.Exit(1)
	}

	handler := server.NewHandler(pipe, counter, logger)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

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
