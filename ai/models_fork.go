package ai

// forkModels are catalog entries this fork carries ahead of the pinned Pi release. Each is the entry
// cmd/gen-models writes from a later published @earendil-works/pi-ai, so GeneratedModels stays
// byte-identical to the pinned package and its drift tests hold. The registry indexes these after
// GeneratedModels, and ListModels("") lists them last. An entry is dropped once the pinned catalog
// has it, which TestForkModelsAreNotInTheGeneratedCatalog enforces.
var forkModels = []GeneratedModel{
	// Claude Haiku 5.5, as @earendil-works/pi-ai 1.1.0 providers/data/anthropic.json defines it.
	{
		ID:                   "claude-haiku-5-5",
		Provider:             "anthropic",
		DisplayName:          "Claude Haiku 5.5",
		API:                  "anthropic-messages",
		BaseURL:              "https://api.anthropic.com",
		Compat:               &ModelCompat{SupportsStrictTools: ptrBool(true), ForceAdaptiveThinking: ptrBool(true), SupportsTemperature: ptrBool(false), SupportsMidConvoEffort: ptrBool(true), SupportsMidConvoSystemMessages: ptrBool(true), SupportsMidConvoToolChanges: ptrBool(true)},
		ThinkingLevelMap:     map[ThinkingLevel]*string{ThinkingLevel("high"): ptrString("high"), ThinkingLevel("low"): ptrString("low"), ThinkingLevel("max"): ptrString("max"), ThinkingLevel("medium"): ptrString("medium"), ThinkingLevel("minimal"): nil, ThinkingLevel("off"): nil, ThinkingLevel("xhigh"): ptrString("xhigh")},
		PromptCache:          ModelPromptCache{"long": 3600, "short": 300},
		InputLimits:          &ModelInputLimits{MaxRequestBytes: 33554432, Images: &ModelImageInputLimits{Resize: &ModelImageResizeOptions{MaxWidth: 2000, MaxHeight: 2000, MaxBytes: 4718592, JPEGQuality: 80}, MaxPerRequest: 600}},
		ContextWindow:        1000000,
		MaxOutputTokens:      128000,
		InputCostPerMTokens:  0.1,
		OutputCostPerMTokens: 0.5,
		CacheReadCost:        0.01,
		CacheWriteCost:       0.125,
		Tiers:                []CostTier{{InputTokensAbove: 100000, InputCostPer1M: 0.5, OutputCostPer1M: 2.5, CacheReadCostPer1M: 0.05, CacheWriteCostPer1M: 0.625}},
		Reasoning:            true,
		Capabilities:         []string{"text", "image"},
	},
}
