package harness

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeFixtureModelUsage(t *testing.T) {
	r := replayClaude(t, "claude-agent-acp-bypass-write.jsonl", RoleBuilder)
	var got []ModelUsage
	for _, e := range r.of(EventUsage) {
		got = append(got, e.Usage.Models...)
	}
	want := []ModelUsage{{Model: "claude-haiku-4-5-20251001", InputTokens: 925, OutputTokens: 349, CacheReadTokens: 38370, CacheWriteTokens: 45121}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %+v, want %+v", got, want)
	}
}

func TestUsageNeverSerializesAgentCost(t *testing.T) {
	b, _ := json.Marshal(Usage{InputTokens: 1, CostUSD: 42})
	if strings.Contains(string(b), "42") || strings.Contains(strings.ToLower(string(b)), "cost") {
		t.Fatalf("usage json = %s", b)
	}
}
