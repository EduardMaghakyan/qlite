package pricing

// priceEntry holds per-token prices in USD.
type priceEntry struct {
	InputPerToken  float64
	OutputPerToken float64
}

// prices maps model names to their per-token pricing.
// Prices are in USD per token (not per 1M tokens).
var prices = map[string]priceEntry{
	"gpt-4o": {
		InputPerToken:  2.50 / 1_000_000,
		OutputPerToken: 10.00 / 1_000_000,
	},
	"gpt-4o-mini": {
		InputPerToken:  0.15 / 1_000_000,
		OutputPerToken: 0.60 / 1_000_000,
	},
	"gpt-4.1-nano": {
		InputPerToken:  0.10 / 1_000_000,
		OutputPerToken: 0.40 / 1_000_000,
	},
}

// Calculate returns the cost in USD for the given model and token counts.
// Returns 0 for unknown models.
func Calculate(model string, inputTokens, outputTokens int) float64 {
	p, ok := prices[model]
	if !ok {
		return 0
	}
	return float64(inputTokens)*p.InputPerToken + float64(outputTokens)*p.OutputPerToken
}
