package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig     `yaml:"server"`
	Providers []ProviderConfig `yaml:"providers"`
	Cache     CacheConfig      `yaml:"cache"`
}

type CacheConfig struct {
	Exact    ExactCacheConfig    `yaml:"exact"`
	Semantic SemanticCacheConfig `yaml:"semantic"`
}

type SemanticCacheConfig struct {
	Enabled         bool    `yaml:"enabled"`
	Threshold       float32 `yaml:"threshold"`
	EmbeddingModel  string  `yaml:"embedding_model"`
	EmbeddingURL    string  `yaml:"embedding_url"`
	EmbeddingKey    string  `yaml:"embedding_key"`
	QdrantURL       string  `yaml:"qdrant_url"`
	QdrantAPIKey    string  `yaml:"qdrant_api_key"`
	QdrantCollection string `yaml:"qdrant_collection"`
}

type ExactCacheConfig struct {
	Enabled    bool          `yaml:"enabled"`
	TTL        time.Duration `yaml:"ttl"`
	MaxEntries int           `yaml:"max_entries"`
}

type ServerConfig struct {
	Port         int           `yaml:"port"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

type ProviderConfig struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"`
	BaseURL string   `yaml:"base_url"`
	APIKey  string   `yaml:"api_key"`
	Models  []string `yaml:"models"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	applyDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 30 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 120 * time.Second
	}
	if cfg.Cache.Exact.TTL == 0 {
		cfg.Cache.Exact.TTL = time.Hour
	}
	if cfg.Cache.Exact.MaxEntries == 0 {
		cfg.Cache.Exact.MaxEntries = 10000
	}
	if cfg.Cache.Semantic.Threshold == 0 {
		cfg.Cache.Semantic.Threshold = 0.95
	}
	if cfg.Cache.Semantic.EmbeddingModel == "" {
		cfg.Cache.Semantic.EmbeddingModel = "text-embedding-3-small"
	}
	if cfg.Cache.Semantic.EmbeddingURL == "" {
		cfg.Cache.Semantic.EmbeddingURL = "https://api.openai.com/v1"
	}
	if cfg.Cache.Semantic.QdrantCollection == "" {
		cfg.Cache.Semantic.QdrantCollection = "qlite_cache"
	}
}

func validate(cfg *Config) error {
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535, got %d", cfg.Server.Port)
	}
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("at least one provider must be configured")
	}
	if cfg.Cache.Semantic.Enabled {
		if cfg.Cache.Semantic.QdrantURL == "" {
			return fmt.Errorf("cache.semantic.qdrant_url is required when semantic cache is enabled")
		}
		if cfg.Cache.Semantic.EmbeddingKey == "" {
			return fmt.Errorf("cache.semantic.embedding_key is required when semantic cache is enabled")
		}
	}
	for i, p := range cfg.Providers {
		if p.Name == "" {
			return fmt.Errorf("providers[%d].name is required", i)
		}
		if p.Type == "" {
			return fmt.Errorf("providers[%d].type is required", i)
		}
		if p.BaseURL == "" {
			return fmt.Errorf("providers[%d].base_url is required", i)
		}
		if len(p.Models) == 0 {
			return fmt.Errorf("providers[%d].models must have at least one model", i)
		}
	}
	return nil
}
