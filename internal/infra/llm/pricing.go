package llm

import (
	llmcore "github.com/patricksign/agentclaw/internal/llm"
)

// CostUSD calculates the cost in USD for the given model and token counts.
// Delegates to the core llm package pricing table.
// Returns 0 if the model is not in the pricing table.
func CostUSD(model string, inputTok, outputTok int64) float64 {
	cost, _ := llmcore.CalcCost(model, inputTok, outputTok)
	return cost
}

// LoadPricing loads the pricing table from the given JSON file path.
func LoadPricing(path string) error {
	return llmcore.LoadPricing(path)
}
