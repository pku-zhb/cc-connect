package core

import "testing"

func TestReleaseAgentSessionMovesInactiveOwnership(t *testing.T) {
	sm := NewSessionManager("")
	thread, dm := "slack:C1:t:1", "slack:D1:U1"
	old := sm.GetOrCreateActive(thread)
	old.SetAgentSessionID("codex-a", "codex")
	sm.NewSession(thread, "fresh").SetAgentSessionID("codex-b", "codex")
	sm.GetOrCreateActive(dm)

	if sm.AgentSessionActiveElsewhere("codex-a", dm) {
		t.Fatal("codex-a is no longer the thread's active session")
	}
	if !sm.AgentSessionActiveElsewhere("codex-b", dm) {
		t.Fatal("codex-b is the thread's active session")
	}
	sm.ReleaseAgentSession("codex-a", dm)
	if owner, ok := sm.AgentSessionOwners()["codex-a"]; ok {
		t.Fatalf("codex-a still owned by %q", owner)
	}
	sm.SwitchToAgentSession(dm, "codex-a", "codex", "")
	if owner := sm.AgentSessionOwners()["codex-a"]; owner != dm {
		t.Fatalf("codex-a owner = %q, want %q", owner, dm)
	}
	if owner := sm.AgentSessionOwners()["codex-b"]; owner != thread {
		t.Fatalf("codex-b owner = %q, want %q", owner, thread)
	}
}
