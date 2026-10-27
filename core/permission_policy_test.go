package core

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type policyTestAgent struct {
	stubAgent
	mu         sync.Mutex
	mode       string
	failSwitch bool
	live       *policyTestSession
}

func (*policyTestAgent) UsesSessionControls() bool { return true }
func (a *policyTestAgent) SetMode(v string)        { a.mu.Lock(); a.mode = v; a.mu.Unlock() }
func (a *policyTestAgent) GetMode() string         { a.mu.Lock(); defer a.mu.Unlock(); return a.mode }
func (*policyTestAgent) PermissionModes() []PermissionModeInfo {
	return []PermissionModeInfo{{Key: "allowAll", Name: "Allow all"}, {Key: "promptUnmatched", Name: "Prompt unmatched"}}
}
func (*policyTestAgent) ResolveMode(v string) (string, bool) {
	switch strings.ToLower(v) {
	case "yolo", "allowall":
		return "allowAll", true
	case "promptunmatched":
		return "promptUnmatched", true
	}
	return "", false
}
func (a *policyTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.live = &policyTestSession{cujAgentSession: newCUJAgentSession(), agent: a, mode: a.mode, permissions: make(chan PermissionResult, 4)}
	return a.live, nil
}

type policyTestSession struct {
	*cujAgentSession
	agent       *policyTestAgent
	mode        string // guarded by embedded mu
	permissions chan PermissionResult
}

func (s *policyTestSession) SetLiveMode(mode string) bool {
	s.agent.mu.Lock()
	fail := s.agent.failSwitch
	s.agent.mu.Unlock()
	if fail && mode == "allowAll" {
		return false
	}
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
	return true
}
func (s *policyTestSession) AutoApprovesPermissions() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode == "allowAll"
}
func (s *policyTestSession) Send(text, id string, images []ImageAttachment, files []FileAttachment) error {
	if text != "foreground-tool" && text != "start-background" {
		return s.cujAgentSession.Send(text, id, images, files)
	}
	s.mu.Lock()
	s.sentPrompts = append(s.sentPrompts, text)
	s.mu.Unlock()
	go func() {
		if text == "start-background" {
			s.events <- Event{Type: EventResult, Content: "BACKGROUND_STARTED", Done: true}
			time.Sleep(75 * time.Millisecond)
			s.events <- Event{Type: EventPermissionRequest, RequestID: "background", ToolName: "Bash", ToolInput: "read research material"}
			return
		}
		s.events <- Event{Type: EventPermissionRequest, RequestID: "foreground", ToolName: "Bash", ToolInput: "write test file"}
		select {
		case response := <-s.permissions:
			result := "TOOL_DENIED"
			if response.Behavior == "allow" {
				result = "FOREGROUND_TOOL_RAN"
			}
			s.events <- Event{Type: EventResult, Content: result, Done: true}
		case <-time.After(3 * time.Second):
		}
	}()
	return nil
}
func (s *policyTestSession) RespondPermission(id string, result PermissionResult) error {
	if id == "background" {
		text := "BACKGROUND_DENIED"
		if result.Behavior == "allow" {
			text = "BACKGROUND_TOOL_RAN"
		}
		s.events <- Event{Type: EventText, Content: text}
		s.events <- Event{Type: EventResult, Done: true}
	} else {
		s.permissions <- result
	}
	return nil
}

func newPolicyEnv(t *testing.T) (*cujEnv, *policyTestAgent) {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	a := &policyTestAgent{mode: "promptUnmatched"}
	e := NewEngine("policy", a, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	return &cujEnv{t: t, engine: e, plat: p}, a
}

func testPolicyYoloForegroundAndBackground(t *testing.T) {
	env, _ := newPolicyEnv(t)
	env.userSends("policy", "/yolo")
	controlWait(t, env, "Permission mode switched")
	env.userSends("policy", "foreground-tool")
	controlWait(t, env, "FOREGROUND_TOOL_RAN")
	env.userSends("policy", "start-background")
	controlWait(t, env, "BACKGROUND_STARTED")
	controlWait(t, env, "BACKGROUND_TOOL_RAN")
	if env.sentContains("BACKGROUND_DENIED") || env.sentContains("auto-denied") {
		t.Fatal("confirmed full approval was lost after foreground completion")
	}
}

func testPolicyModeFailureAndPendingApproval(t *testing.T) {
	t.Run("failed selection is not acknowledged", func(t *testing.T) {
		env, a := newPolicyEnv(t)
		env.userSends("policy", "/mode promptUnmatched")
		controlWait(t, env, "Permission mode switched")
		a.mu.Lock()
		a.failSwitch = true
		a.mu.Unlock()
		env.plat.clearSent()
		env.userSends("policy", "/mode yolo")
		controlWait(t, env, "Permission mode change failed")
		if env.sentContains("Permission mode switched") {
			t.Fatal("rejected mode was acknowledged as successful")
		}
		env.userSends("policy", "foreground-tool")
		controlWait(t, env, "write test file")
		env.userSends("policy", "deny")
		controlWait(t, env, "TOOL_DENIED")
	})
	t.Run("yolo resolves an existing tool approval", func(t *testing.T) {
		env, _ := newPolicyEnv(t)
		env.userSends("policy", "hello")
		controlWait(t, env, "ok")
		env.userSends("policy", "foreground-tool")
		controlWait(t, env, "write test file")
		env.userSends("policy", "/yolo")
		controlWait(t, env, "FOREGROUND_TOOL_RAN")
	})
}

func TestYoloAlias_RespectsDisabledModeCommand(t *testing.T) {
	env, a := newPolicyEnv(t)
	env.engine.SetDisabledCommands([]string{"mode"})
	env.userSends("policy", "/yolo")
	controlWait(t, env, "disabled")
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.live != nil {
		t.Fatal("disabled alias reached the agent")
	}
}

func TestPermissionPolicy_YoloDoesNotAnswerUserQuestions(t *testing.T) {
	env, a := newPolicyEnv(t)
	env.userSends("policy", "/yolo")
	controlWait(t, env, "Permission mode switched")
	a.mu.Lock()
	live := a.live
	a.mu.Unlock()
	question := &pendingPermission{RequestID: "question", Questions: []UserQuestion{{Question: "Which report?"}}, Resolved: make(chan struct{})}
	env.engine.interactiveMu.Lock()
	state := env.engine.interactiveStates["test:policy"]
	env.engine.interactiveMu.Unlock()
	state.mu.Lock()
	state.pending = question
	state.mu.Unlock()
	env.engine.reconcilePermissionPolicy("test:policy", live)
	select {
	case <-question.Resolved:
		t.Fatal("permission policy answered a user question")
	default:
	}
	select {
	case <-live.permissions:
		t.Fatal("question was automatically allowed")
	default:
	}
}
