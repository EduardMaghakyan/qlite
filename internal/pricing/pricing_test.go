package pricing

import (
	"math"
	"testing"
)

func TestCalculate_GPT4o(t *testing.T) {
	cost := Calculate("gpt-4o", 1000, 500)
	// Input: 1000 * 2.50/1M = 0.0025
	// Output: 500 * 10.00/1M = 0.005
	// Total: 0.0075
	expected := 0.0075
	if math.Abs(cost-expected) > 1e-10 {
		t.Errorf("expected cost %.10f, got %.10f", expected, cost)
	}
}

func TestCalculate_GPT4oMini(t *testing.T) {
	cost := Calculate("gpt-4o-mini", 1000, 500)
	// Input: 1000 * 0.15/1M = 0.00015
	// Output: 500 * 0.60/1M = 0.0003
	// Total: 0.00045
	expected := 0.00045
	if math.Abs(cost-expected) > 1e-10 {
		t.Errorf("expected cost %.10f, got %.10f", expected, cost)
	}
}

func TestCalculate_GPT41Nano(t *testing.T) {
	cost := Calculate("gpt-4.1-nano", 1000, 500)
	// Input: 1000 * 0.10/1M = 0.0001
	// Output: 500 * 0.40/1M = 0.0002
	// Total: 0.0003
	expected := 0.0003
	if math.Abs(cost-expected) > 1e-10 {
		t.Errorf("expected cost %.10f, got %.10f", expected, cost)
	}
}

func TestCalculate_UnknownModel(t *testing.T) {
	cost := Calculate("unknown-model", 1000, 500)
	if cost != 0 {
		t.Errorf("expected 0 for unknown model, got %f", cost)
	}
}

func TestCalculate_ZeroTokens(t *testing.T) {
	cost := Calculate("gpt-4o", 0, 0)
	if cost != 0 {
		t.Errorf("expected 0 for zero tokens, got %f", cost)
	}
}
