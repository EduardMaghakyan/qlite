package tokenizer

import (
	"testing"

	"github.com/eduardmaghakyan/qlite/internal/model"
)

func TestCounter_CountMessages_KnownModel(t *testing.T) {
	counter := NewCounter()
	messages := []model.Message{
		{Role: "user", Content: "Hello, how are you?"},
	}

	tokens := counter.CountMessages("gpt-4o", messages)
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}
	// "Hello, how are you?" should be roughly 5-6 tokens plus overhead.
	if tokens > 20 {
		t.Errorf("token count seems too high: %d", tokens)
	}
}

func TestCounter_CountMessages_MultipleMessages(t *testing.T) {
	counter := NewCounter()
	messages := []model.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "What is 2+2?"},
	}

	tokens := counter.CountMessages("gpt-4o", messages)
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}
}

func TestCounter_CountMessages_UnknownModel(t *testing.T) {
	counter := NewCounter()
	messages := []model.Message{
		{Role: "user", Content: "Hello world this is a test"},
	}

	tokens := counter.CountMessages("unknown-model", messages)
	// Fallback: len("Hello world this is a test") / 4 = 26/4 = 6
	if tokens != 6 {
		t.Errorf("expected fallback count of 6, got %d", tokens)
	}
}

func TestCounter_CountText(t *testing.T) {
	counter := NewCounter()

	tokens := counter.CountText("gpt-4o", "Hello world")
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}

	// Unknown model fallback.
	tokens = counter.CountText("unknown-model", "Hello world!")
	expected := len("Hello world!") / 4 // 12/4 = 3
	if tokens != expected {
		t.Errorf("expected %d, got %d", expected, tokens)
	}
}

func TestCounter_CountMessages_GPT4_1Nano(t *testing.T) {
	counter := NewCounter()
	messages := []model.Message{
		{Role: "user", Content: "Hello"},
	}

	tokens := counter.CountMessages("gpt-4.1-nano", messages)
	if tokens <= 0 {
		t.Errorf("expected positive token count for gpt-4.1-nano, got %d", tokens)
	}
}
