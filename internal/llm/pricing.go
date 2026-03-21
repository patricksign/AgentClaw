package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// CostMode controls how token costs are calculated.
type CostMode string

const (
	CostModeStandard   CostMode = ""            // default: standard input/output pricing
	CostModeCacheWrite CostMode = "cache_write" // writing to prompt cache (5 min TTL)
	CostModeCacheHit   CostMode = "cache_hit"   // reading from prompt cache
	CostModeBatch      CostMode = "batch"       // batch API (50% discount, async)
)

// pricing holds per-million-token costs for a single model.
type pricing struct {
	InputPer1M        float64
	OutputPer1M       float64
	CacheWrite5mPer1M float64 // ephemeral cache write (5 min TTL)
	CacheWrite1hPer1M float64 // persistent cache write (1 hr TTL)
	CacheHitPer1M     float64 // cache read hit
	BatchInputPer1M   float64 // batch API input
	BatchOutputPer1M  float64 // batch API output
}

// pricingTable is the runtime pricing lookup, keyed by internal model alias
// (e.g. "opus", "sonnet", "minimax", "glm5", "glm-flash").
var (
	pricingMu    sync.RWMutex
	pricingTable = map[string]pricing{} // populated by LoadPricing
)

// aliasToJSONModel maps the internal model alias used by Router.Call() to
// the exact "name" field in agent-pricing.json.
// When the JSON file adds or renames models, update this map.
var aliasToJSONModel = map[string]struct {
	provider string
	model    string
}{
	"opus":      {provider: "Anthropic", model: "Claude Opus 4.6"},
	"sonnet":    {provider: "Anthropic", model: "Claude Sonnet 4.6"},
	"haiku":     {provider: "Anthropic", model: "Claude Haiku 4.5"},
	"minimax":   {provider: "Minimax", model: "MiniMax-M2.5"},
	"glm5":      {provider: "Z.AI (Zhipu)", model: "GLM-5"},
	"glm-flash": {provider: "Z.AI (Zhipu)", model: "GLM-4.5-Flash"},
	"kimi":      {provider: "MoonshotAI", model: "Kimi K2.5"},
}

// ─── JSON schema ─────────────────────────────────────────────────────────────

type pricingFile map[string]providerBlock // keyed by provider name

type providerBlock struct {
	Models []modelEntry `json:"models"`
}

type modelEntry struct {
	Name    string      `json:"name"`
	Pricing modelPrices `json:"pricing"`
}

type modelPrices struct {
	Input        *float64 `json:"input"`
	Output       *float64 `json:"output"`
	CacheWrite5m *float64 `json:"cache_write_5m"`
	CacheWrite1h *float64 `json:"cache_write_1h"`
	CacheHit     *float64 `json:"cache_hit"`
	BatchInput   *float64 `json:"batch_input"`
	BatchOutput  *float64 `json:"batch_output"`
	// GLM/MiniMax variants
	CachedInput *float64 `json:"cached_input"`
	CacheRead   *float64 `json:"cache_read"`
	CacheWrite  *float64 `json:"cache_write"`
}

// ─── Loader ──────────────────────────────────────────────────────────────────

// LoadPricing reads the agent-pricing.json file and populates the runtime
// pricing table. It must be called once at startup before any CalcCost call.
// Aliases not found in the file get zero pricing (logged as warnings).
func LoadPricing(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pricing file: %w", err)
	}

	var file pricingFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse pricing file: %w", err)
	}

	// Build a lookup: "provider|model_lower" → modelPrices.
	lookup := make(map[string]modelPrices)
	for providerName, block := range file {
		for _, m := range block.Models {
			key := providerName + "|" + strings.ToLower(m.Name)
			lookup[key] = m.Pricing
		}
	}

	table := make(map[string]pricing, len(aliasToJSONModel))
	for alias, ref := range aliasToJSONModel {
		key := ref.provider + "|" + strings.ToLower(ref.model)
		mp, ok := lookup[key]
		if !ok {
			// Zero pricing — model not found in file.
			table[alias] = pricing{}
			continue
		}
		var inp, outp float64
		if mp.Input != nil {
			inp = *mp.Input
		}
		if mp.Output != nil {
			outp = *mp.Output
		}
		p := pricing{InputPer1M: inp, OutputPer1M: outp}
		// Anthropic-style cache pricing
		if mp.CacheWrite5m != nil {
			p.CacheWrite5mPer1M = *mp.CacheWrite5m
		}
		if mp.CacheWrite1h != nil {
			p.CacheWrite1hPer1M = *mp.CacheWrite1h
		}
		if mp.CacheHit != nil {
			p.CacheHitPer1M = *mp.CacheHit
		}
		if mp.BatchInput != nil {
			p.BatchInputPer1M = *mp.BatchInput
		}
		if mp.BatchOutput != nil {
			p.BatchOutputPer1M = *mp.BatchOutput
		}
		// GLM/MiniMax cache pricing (mapped to CacheHit)
		if mp.CachedInput != nil && p.CacheHitPer1M == 0 {
			p.CacheHitPer1M = *mp.CachedInput
		}
		if mp.CacheRead != nil && p.CacheHitPer1M == 0 {
			p.CacheHitPer1M = *mp.CacheRead
		}
		if mp.CacheWrite != nil && p.CacheWrite5mPer1M == 0 {
			p.CacheWrite5mPer1M = *mp.CacheWrite
		}
		table[alias] = p
	}

	pricingMu.Lock()
	pricingTable = table
	pricingMu.Unlock()

	return nil
}

// PricingEntry is a public view of a model's pricing for API responses.
type PricingEntry struct {
	Alias       string  `json:"alias"`
	InputPer1M  float64 `json:"input_per_1m"`
	OutputPer1M float64 `json:"output_per_1m"`
}

// GetPricingTable returns the current pricing table for API responses.
func GetPricingTable() []PricingEntry {
	pricingMu.RLock()
	defer pricingMu.RUnlock()
	entries := make([]PricingEntry, 0, len(pricingTable))
	for alias, p := range pricingTable {
		entries = append(entries, PricingEntry{
			Alias:       alias,
			InputPer1M:  p.InputPer1M,
			OutputPer1M: p.OutputPer1M,
		})
	}
	return entries
}

// ErrModelNotFound indicates the model alias is not in the loaded pricing table.
var ErrModelNotFound = fmt.Errorf("llm: model not found in pricing table")

// CalcCost returns the estimated USD cost for the given token counts.
// Returns (cost, nil) on success, or (0, ErrModelNotFound) for unknown models.
func CalcCost(model string, in, out int64) (float64, error) {
	pricingMu.RLock()
	p, ok := pricingTable[model]
	pricingMu.RUnlock()
	if !ok {
		return 0, ErrModelNotFound
	}
	return float64(in)/1e6*p.InputPer1M + float64(out)/1e6*p.OutputPer1M, nil
}

// CalcCostAdvanced returns the estimated USD cost using the specified mode.
// cacheTokens is the number of tokens served from cache (subset of in).
// Returns (cost, nil) on success, or (0, ErrModelNotFound) for unknown models.
func CalcCostAdvanced(model string, in, out, cacheTokens int64, mode CostMode) (float64, error) {
	pricingMu.RLock()
	p, ok := pricingTable[model]
	pricingMu.RUnlock()
	if !ok {
		return 0, ErrModelNotFound
	}

	switch mode {
	case CostModeBatch:
		inPrice := p.BatchInputPer1M
		if inPrice == 0 {
			inPrice = p.InputPer1M // fallback if batch pricing not available
		}
		outPrice := p.BatchOutputPer1M
		if outPrice == 0 {
			outPrice = p.OutputPer1M
		}
		return float64(in)/1e6*inPrice + float64(out)/1e6*outPrice, nil

	case CostModeCacheHit, CostModeCacheWrite:
		// Non-cached input tokens use standard pricing.
		regularIn := in - cacheTokens
		if regularIn < 0 {
			regularIn = 0
		}
		cost := float64(regularIn) / 1e6 * p.InputPer1M

		// Cached portion uses cache pricing.
		if cacheTokens > 0 {
			if mode == CostModeCacheHit && p.CacheHitPer1M > 0 {
				cost += float64(cacheTokens) / 1e6 * p.CacheHitPer1M
			} else if mode == CostModeCacheWrite && p.CacheWrite5mPer1M > 0 {
				cost += float64(cacheTokens) / 1e6 * p.CacheWrite5mPer1M
			} else {
				cost += float64(cacheTokens) / 1e6 * p.InputPer1M
			}
		}

		cost += float64(out) / 1e6 * p.OutputPer1M
		return cost, nil

	default:
		return float64(in)/1e6*p.InputPer1M + float64(out)/1e6*p.OutputPer1M, nil
	}
}

// GetModelPricing returns the full pricing for a model alias (for API responses).
func GetModelPricing(alias string) (p pricing, ok bool) {
	pricingMu.RLock()
	p, ok = pricingTable[alias]
	pricingMu.RUnlock()
	return
}
