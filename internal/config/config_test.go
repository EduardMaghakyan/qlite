package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	content := `
server:
  port: 9090
  read_timeout: 10s
  write_timeout: 60s
providers:
  - name: openai
    type: openai
    base_url: https://api.openai.com/v1
    api_key: sk-test
    models:
      - gpt-4o
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(cfg.Providers))
	}
	if cfg.Providers[0].Name != "openai" {
		t.Errorf("expected provider name 'openai', got %q", cfg.Providers[0].Name)
	}
}

func TestLoad_Defaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	content := `
providers:
  - name: openai
    type: openai
    base_url: https://api.openai.com/v1
    api_key: sk-test
    models:
      - gpt-4o
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Server.Port)
	}
}

func TestLoad_EnvExpansion(t *testing.T) {
	t.Setenv("TEST_API_KEY", "sk-expanded")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	content := `
providers:
  - name: openai
    type: openai
    base_url: https://api.openai.com/v1
    api_key: ${TEST_API_KEY}
    models:
      - gpt-4o
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Providers[0].APIKey != "sk-expanded" {
		t.Errorf("expected expanded key 'sk-expanded', got %q", cfg.Providers[0].APIKey)
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "no providers",
			content: `server: {port: 8080}`,
		},
		{
			name: "missing provider name",
			content: `
providers:
  - type: openai
    base_url: https://api.openai.com/v1
    api_key: sk-test
    models: [gpt-4o]`,
		},
		{
			name: "missing provider type",
			content: `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-test
    models: [gpt-4o]`,
		},
		{
			name: "missing base_url",
			content: `
providers:
  - name: openai
    type: openai
    api_key: sk-test
    models: [gpt-4o]`,
		},
		{
			name: "no models",
			content: `
providers:
  - name: openai
    type: openai
    base_url: https://api.openai.com/v1
    api_key: sk-test`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(configPath)
			if err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}
