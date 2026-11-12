package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubResolvingModelAgent struct {
	stubModelModeAgent
	inputs []string
}

func (a *stubResolvingModelAgent) ResolveModel(_ context.Context, input string) (string, error) {
	a.inputs = append(a.inputs, input)
	switch input {
	case "argon high":
		return "gemini-4-argon-high", nil
	case "argon":
		return "gemini-4-argon-medium", nil
	}
	return "", errors.New("unknown agy model \"" + input + "\"; use one of: carbon, argon")
}

func TestCmdModel_ResolverExpandsShortFormWithEffort(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubResolvingModelAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdModel(p, msg, []string{"switch", "argon", "high"})

	if agent.model != "gemini-4-argon-high" {
		t.Fatalf("agent model = %q, want gemini-4-argon-high", agent.model)
	}
	if len(agent.inputs) != 1 || agent.inputs[0] != "argon high" {
		t.Fatalf("resolver inputs = %v", agent.inputs)
	}
}

func TestCmdModel_ResolverRejectsUnknownModel(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubResolvingModelAgent{}
	agent.model = "gemini-4-carbon-medium"
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdModel(p, msg, []string{"switch", "argonx"})

	if agent.model != "gemini-4-carbon-medium" {
		t.Fatalf("unknown model changed agent model to %q", agent.model)
	}
	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "unknown agy model") {
		t.Fatalf("expected rejection reply, got %v", sent)
	}
}
