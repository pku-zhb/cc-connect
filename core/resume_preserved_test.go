package core

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

type resumePreservedJourneyAgent struct {
	reasoningJourneyAgent
	resumeMu sync.Mutex
	blocked  bool
	attempts []string
}

func (a *resumePreservedJourneyAgent) StartSession(_ context.Context, id string) (AgentSession, error) {
	a.resumeMu.Lock()
	defer a.resumeMu.Unlock()
	a.attempts = append(a.attempts, id)
	if a.blocked {
		return nil, &SessionResumeError{Cause: fmt.Errorf("thread already has an active writer"), InUse: true}
	}
	s := &resumePreservedJourneySession{cujAgentSession: newCUJAgentSession(), id: id}
	s.reply = "resumed original conversation"
	return s, nil
}

type resumePreservedJourneySession struct {
	*cujAgentSession
	id string
}

func (s *resumePreservedJourneySession) CurrentSessionID() string { return s.id }

func newResumePreservedJourney(t *testing.T) (*cujEnv, *resumePreservedJourneyAgent) {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	a := &resumePreservedJourneyAgent{blocked: true}
	e := NewEngine("resume", a, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	s := e.sessions.GetOrCreateActive("test:user")
	s.SetAgentSessionID("original-thread", a.Name())
	s.AddHistory("user", "original-context")
	return &cujEnv{t: t, engine: e, plat: p}, a
}

func TestResumeFailure_PreservesNativeConversationInsteadOfStartingFresh(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprintf("busy=%v", busy), func(t *testing.T) {
			a := &stubStartSessionAgent{failIDs: map[string]error{
				"original-thread": &SessionResumeError{Cause: fmt.Errorf("temporarily unavailable"), InUse: busy},
			}}
			p := &stubPlatformEngine{n: "test"}
			e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
			defer e.Stop()
			s := e.sessions.GetOrCreateActive("test:user")
			s.SetAgentSessionID("original-thread", "stub")
			s.AddHistory("user", "original context")
			for i := 0; i < 2; i++ {
				state := e.getOrCreateInteractiveStateWith("test:user", p, "ctx", s, e.sessions, nil, "")
				if state.agentSession != nil {
					t.Fatal("resume failure silently started a fresh native session")
				}
			}
			if s.GetAgentSessionID() != "original-thread" || len(s.GetHistory(0)) != 1 {
				t.Fatal("resume failure detached the original conversation")
			}
			if len(a.calls) != 2 || a.calls[0] != "original-thread" || a.calls[1] != "original-thread" {
				t.Fatalf("resume attempts = %v", a.calls)
			}
		})
	}
}
