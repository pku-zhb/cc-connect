package core

import (
	"strings"
	"testing"
)

// serialQueuingSession models an agent that starts one process or one
// blocking prompt per turn and therefore cannot take input mid-turn.
type serialQueuingSession struct {
	*queuingAgentSession
}

func (serialQueuingSession) AcceptsMidTurnInput() bool { return false }

func TestCmdPs_BusySerialSession_QueuesInsteadOfConcurrentSend(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	inner := newQueuingSession("ps-serial")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	state := &interactiveState{agentSession: serialQueuingSession{inner}, platform: p}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	session := e.sessions.GetOrCreateActive(key)
	if _, lockedNow := session.TryLock(); !lockedNow {
		t.Fatal("expected TryLock to succeed")
	}
	defer session.Unlock(0)

	msg := &Message{SessionKey: key, Content: "/ps keep the bar high", ReplyCtx: "ctx", MessageID: "m2"}
	e.cmdPs(p, msg, []string{"keep", "the", "bar", "high"})

	inner.sendMu.Lock()
	calls := len(inner.sendCalls)
	inner.sendMu.Unlock()
	if calls != 0 {
		t.Fatalf("serial session must not receive a concurrent Send, got %d call(s)", calls)
	}

	state.mu.Lock()
	pending := append([]queuedMessage(nil), state.pendingMessages...)
	state.mu.Unlock()
	if len(pending) != 1 || pending[0].content != "keep the bar high" || pending[0].messageID != "m2" {
		t.Fatalf("expected the /ps text queued as the next turn, got %+v", pending)
	}
	if msg.Content != "/ps keep the bar high" {
		t.Fatalf("original message mutated: %q", msg.Content)
	}

	found := false
	for _, s := range p.getSent() {
		if strings.Contains(s, e.i18n.T(MsgMessageQueued)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected queued reply, got %v", p.getSent())
	}
}
