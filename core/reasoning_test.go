package core

import (
	"context"
	"path/filepath"
	"testing"
)

// Reuse the model boundary that echoes each turn's current selection.
type reasoningJourneyAgent struct{ fastTestAgent }

func (*reasoningJourneyAgent) SupportsLiveReasoningEffort() bool { return true }
func (a *reasoningJourneyAgent) SetReasoningEffort(v string)     { a.SetServiceTier(v) }
func (a *reasoningJourneyAgent) GetReasoningEffort() string {
	v, _ := a.GetServiceTier(context.Background())
	return v
}
func (*reasoningJourneyAgent) AvailableReasoningEfforts() []string {
	return []string{"high", "max", "ultra"}
}

type liveReasoningTestAgent struct{ stubModelModeAgent }

func (*liveReasoningTestAgent) SupportsLiveReasoningEffort() bool { return true }

func TestReasoningChange_PreservesLiveSessionAndHistory(t *testing.T) {
	for _, card := range []bool{false, true} {
		t.Run(map[bool]string{false: "command", true: "card"}[card], func(t *testing.T) {
			p := &stubPlatformEngine{n: "plain"}
			a := &liveReasoningTestAgent{}
			e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
			defer e.Stop()
			const key = "plain:user"
			s := e.sessions.GetOrCreateActive(key)
			s.SetAgentSessionID("existing-thread", "test")
			s.AddHistory("user", "keep this context")
			live := newCUJAgentSession()
			e.interactiveStates[key] = &interactiveState{agent: a, agentSession: live}
			if card {
				e.executeCardAction("/reasoning", "high", key)
			} else {
				e.cmdReasoning(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{"high"})
			}
			if s.GetAgentSessionID() != "existing-thread" || len(s.GetHistory(0)) != 1 {
				t.Fatal("reasoning switch detached the conversation or erased history")
			}
			if live.closed.Get() {
				t.Fatal("reasoning switch closed the live native session")
			}
		})
	}
}

func TestReasoningChange_PreservesDormantSessionAndWorkspaceChoice(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	a := &liveReasoningTestAgent{}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	stateFile := filepath.Join(t.TempDir(), "state.json")
	e.SetProjectStateStore(NewProjectStateStore(stateFile))
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))
	workspace := normalizeWorkspacePath(t.TempDir())
	e.workspaceBindings.Bind("project:test", "C-reasoning", "channel", workspace)
	ws := e.workspacePool.GetOrCreate(workspace)
	ws.agent = &liveReasoningTestAgent{}
	ws.sessions = NewSessionManager("")
	const key = "feishu:C-reasoning:user"
	s := ws.sessions.GetOrCreateActive(key)
	s.SetAgentSessionID("dormant-thread", "test")
	s.AddHistory("user", "original context")
	e.cmdReasoning(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{"high"})
	if s.GetAgentSessionID() != "dormant-thread" || len(s.GetHistory(0)) != 1 {
		t.Fatal("reasoning switch lost the dormant conversation")
	}
	if got := NewProjectStateStore(stateFile).WorkspaceReasoningOverride(workspace); got != "high" {
		t.Fatalf("persisted workspace effort = %q", got)
	}
	if a.GetReasoningEffort() != "" {
		t.Fatal("workspace effort leaked into the root agent")
	}
}
