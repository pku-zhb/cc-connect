package core

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// steeringSession models Codex app-server: input reaches a running turn via
// SteerUserInput (turn/steer); Send would start a separate turn.
type steeringSession struct {
	*queuingAgentSession
	mu      sync.Mutex
	active  bool
	steered []string
}

func (s *steeringSession) SteerUserInput(prompt string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return false, nil
	}
	s.steered = append(s.steered, prompt)
	return true, nil
}

func setupOverriddenTopic(t *testing.T) (*Engine, string, string) {
	t.Helper()
	e, _, _, shared := newScopedEngine(t)
	// Bindings store canonical paths; use them so macOS /var -> /private/var
	// temp dirs do not hide the override.
	if real, err := filepath.EvalSymlinks(shared); err == nil {
		shared = real
	}
	bindScopedTopic(e, "om_root_a", shared)
	override := filepath.Join(filepath.Dir(shared), "repo-alpha")
	if err := os.MkdirAll(override, 0o755); err != nil {
		t.Fatal(err)
	}
	e.projectState.SetWorkspaceDirOverride(shared+":"+topicKeyA, override)
	e.projectState.Save()
	_, _, _, effective, err := e.workspaceContext(shared, topicKeyA)
	if err != nil {
		t.Fatal(err)
	}
	return e, shared, effective + ":" + topicKeyA
}

// Commands must find the running turn under the same key incoming messages
// use; with a /dir override that is "<override dir>:<key>", not the binding.
func TestInteractiveKeyForSessionKey_FollowsDirOverride(t *testing.T) {
	e, shared, messageKey := setupOverriddenTopic(t)
	if strings.HasPrefix(messageKey, shared+":") {
		t.Fatalf("override not applied on the message path: %q", messageKey)
	}
	if got := e.interactiveKeyForSessionKey(topicKeyA); got != messageKey {
		t.Fatalf("command key %q, message key %q", got, messageKey)
	}
}

func TestCmdPs_OverriddenWorkspaceSteersRunningTurn(t *testing.T) {
	e, _, messageKey := setupOverriddenTopic(t)
	p := &stubPlatformEngine{n: "feishu"}
	sess := &steeringSession{queuingAgentSession: newQueuingSession("codex-thread"), active: true}
	e.interactiveMu.Lock()
	e.interactiveStates[messageKey] = &interactiveState{agentSession: sess, platform: p}
	e.interactiveMu.Unlock()

	_, sessions := e.sessionContextForKey(topicKeyA)
	session := sessions.GetOrCreateActive(topicKeyA)
	if _, ok := session.TryLock(); !ok {
		t.Fatal("lock")
	}
	defer session.Unlock(0)

	msg := &Message{SessionKey: topicKeyA, Content: "/ps also check the logs", ReplyCtx: "ctx"}
	e.cmdPs(p, msg, []string{"also", "check", "the", "logs"})

	sess.mu.Lock()
	steered := append([]string(nil), sess.steered...)
	sess.mu.Unlock()
	if len(steered) != 1 || steered[0] != "also check the logs" {
		t.Fatalf("steered=%v sent=%v replies=%v", steered, sess.sendCalls, p.getSent())
	}
	if len(sess.sendCalls) != 0 {
		t.Fatalf("Send must not start a second turn: %v", sess.sendCalls)
	}
	if sent := p.getSent(); len(sent) == 0 || !strings.Contains(sent[len(sent)-1], e.i18n.T(MsgPsSent)) {
		t.Fatalf("expected MsgPsSent, got %v", sent)
	}
}

// When the agent reports no active turn, /ps keeps the old behaviour.
func TestCmdPs_SteerWithoutActiveTurnFallsBackToSend(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := &steeringSession{queuingAgentSession: newQueuingSession("codex-idle")}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	key := "test:user1"
	e.interactiveMu.Lock()
	e.interactiveStates[key] = &interactiveState{agentSession: sess, platform: p}
	e.interactiveMu.Unlock()
	session := e.sessions.GetOrCreateActive(key)
	if _, ok := session.TryLock(); !ok {
		t.Fatal("lock")
	}
	defer session.Unlock(0)

	e.cmdPs(p, &Message{SessionKey: key, Content: "/ps hi", ReplyCtx: "ctx"}, []string{"hi"})
	sess.sendMu.Lock()
	defer sess.sendMu.Unlock()
	if len(sess.sendCalls) != 1 || sess.sendCalls[0] != "hi" {
		t.Fatalf("fallback Send not used: %v", sess.sendCalls)
	}
}
