package usecase

import (
	"context"
	"testing"

	"legos-factory/backend/internal/agent"
	"legos-factory/backend/internal/model"
)

type noMCPHarness struct{ *agent.MockHarness }

func (noMCPHarness) SupportsMCP() bool { return false }

func TestChatModelListFollowsTheChatOwner(t *testing.T) {
	var seen []string
	c := &Chat{Models: func(ctx context.Context) ([]model.ModelInfo, error) {
		seen = append(seen, ChatUser(ctx))
		return []model.ModelInfo{{ProviderID: "p", ModelID: "m"}}, nil
	}}
	for _, u := range []string{"u1", "u2", "u1"} {
		if _, err := c.ModelList(WithChatUser(context.Background(), u)); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0] != "u1" || seen[1] != "u2" {
		t.Fatalf("lists must be fetched and cached per user: %v", seen)
	}
}

func TestHarnessTakesMCP(t *testing.T) {
	if !harnessTakesMCP(agent.NewMock()) || harnessTakesMCP(noMCPHarness{agent.NewMock()}) {
		t.Fatal("harnesses that start no MCP servers must not be counted as using them")
	}
}
