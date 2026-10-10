package ai

import (
	"reflect"
	"testing"
)

// isForkModel reports whether model is one of the fork's entries, which fixtures of the pinned Pi catalog lack.
func isForkModel(model GeneratedModel) bool {
	for _, fork := range forkModels {
		if fork.Provider == model.Provider && fork.ID == model.ID {
			return true
		}
	}
	return false
}

// TestForkModelsAreNotInTheGeneratedCatalog fails once the pinned Pi catalog carries a fork entry, so the
// fork's copy is dropped rather than shadowing the generated one.
func TestForkModelsAreNotInTheGeneratedCatalog(t *testing.T) {
	generated := make(map[string]bool, len(GeneratedModels))
	for _, m := range GeneratedModels {
		generated[m.Provider+"/"+m.ID] = true
	}
	seen := map[string]bool{}
	for _, m := range forkModels {
		fq := m.Provider + "/" + m.ID
		if generated[fq] {
			t.Errorf("%s is in the generated catalog now; drop it from forkModels", fq)
		}
		if seen[fq] {
			t.Errorf("%s is listed twice in forkModels", fq)
		}
		seen[fq] = true
	}
}

// TestForkModelsAreInTheRegistry checks each fork entry resolves exactly, lists under its provider and
// after the generated catalog in ListModels("").
func TestForkModelsAreInTheRegistry(t *testing.T) {
	all := ListModels("")
	if got, want := len(all), len(GeneratedModels)+len(forkModels); got != want {
		t.Fatalf("ListModels(\"\") = %d models, want %d", got, want)
	}
	for i, m := range forkModels {
		fq := m.Provider + "/" + m.ID
		got, ok := LookupModelExact(fq)
		if !ok || !reflect.DeepEqual(*got, m) {
			t.Errorf("LookupModelExact(%q) = %+v, %t", fq, got, ok)
		}
		if !reflect.DeepEqual(all[len(GeneratedModels)+i], m) {
			t.Errorf("ListModels(\"\")[%d] = %s, want %s", len(GeneratedModels)+i, all[len(GeneratedModels)+i].ID, m.ID)
		}
		listed := false
		for _, p := range ListModels(m.Provider) {
			listed = listed || p.ID == m.ID
		}
		if !listed {
			t.Errorf("ListModels(%q) lacks %s", m.Provider, m.ID)
		}
	}
}

// TestClaudeHaiku55 pins the published values: pi-ai 1.1.0 providers/data/anthropic.json, which match
// Anthropic's models overview and pricing pages (1M context, 128K output, adaptive thinking with all five
// effort levels, $0.10/$0.50 base with a higher tier for prompts over 100,000 tokens).
func TestClaudeHaiku55(t *testing.T) {
	g, ok := LookupModelExact("anthropic/claude-haiku-5-5")
	if !ok {
		t.Fatal("anthropic/claude-haiku-5-5 missing")
	}
	if g.API != APIAnthropicMessages || g.ContextWindow != 1000000 || g.MaxOutputTokens != 128000 || !g.Reasoning {
		t.Fatalf("model = %+v", g)
	}
	if g.InputCostPerMTokens != 0.1 || g.OutputCostPerMTokens != 0.5 || g.CacheReadCost != 0.01 || g.CacheWriteCost != 0.125 {
		t.Fatalf("base cost = %v/%v/%v/%v", g.InputCostPerMTokens, g.OutputCostPerMTokens, g.CacheReadCost, g.CacheWriteCost)
	}
	wantTiers := []CostTier{{InputTokensAbove: 100000, InputCostPer1M: 0.5, OutputCostPer1M: 2.5, CacheReadCostPer1M: 0.05, CacheWriteCostPer1M: 0.625}}
	if !reflect.DeepEqual(g.Tiers, wantTiers) {
		t.Fatalf("tiers = %+v", g.Tiers)
	}
	m := g.ToModel()
	var levels []ThinkingLevel
	for _, level := range []ThinkingLevel{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if ClampThinkingLevel(m, level) == level {
			levels = append(levels, level)
		}
	}
	if want := []ThinkingLevel{"low", "medium", "high", "xhigh", "max"}; !reflect.DeepEqual(levels, want) {
		t.Fatalf("thinking levels = %v, want %v", levels, want)
	}
	if m.ProviderMeta.Compat == nil || m.ProviderMeta.Compat.ForceAdaptiveThinking == nil || !*m.ProviderMeta.Compat.ForceAdaptiveThinking {
		t.Fatal("Haiku 5.5 must use adaptive thinking")
	}
}
