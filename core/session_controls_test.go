package core

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type controlTestSession struct {
	*cujAgentSession
	configMu      sync.Mutex
	model, effort string
}

func (s *controlTestSession) ConfigOptions() []SessionConfigOption {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return []SessionConfigOption{
		{ID: "engine", Name: "Model", Category: "model", CurrentValue: s.model, Options: []SessionConfigValue{{Value: "m1"}, {Value: "m2"}}},
		{ID: "budget", Name: "Effort", Category: "thought_level", CurrentValue: s.effort, Options: []SessionConfigValue{{Value: "low"}, {Value: "high"}}},
	}
}
func (s *controlTestSession) SetConfigOption(_ context.Context, id, value string) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if id == "engine" && (value == "m1" || value == "m2") {
		s.model = value
		return nil
	}
	if id == "budget" && (value == "low" || value == "high") {
		s.effort = value
		return nil
	}
	return fmt.Errorf("unsupported selection")
}
func (s *controlTestSession) Send(text, id string, images []ImageAttachment, files []FileAttachment) error {
	s.configMu.Lock()
	reply := s.model + "/" + s.effort + ":" + text
	s.configMu.Unlock()
	s.mu.Lock()
	s.reply = reply
	s.mu.Unlock()
	return s.cujAgentSession.Send(text, id, images, files)
}
func (*controlTestSession) GetUsage(context.Context) (*UsageReport, error) {
	return &UsageReport{Provider: "Test account", Buckets: []UsageBucket{{Windows: []UsageWindow{
		{UsedPercent: 23, WindowSeconds: 18000, ResetAtUnix: 2000000000},
		{UsedPercent: 42, WindowSeconds: 604800, ResetAtUnix: 2000500000},
	}}}}, nil
}

func (*controlTestSession) GetSessionUsage() *SessionUsage {
	used, size := int64(42), int64(100)
	return &SessionUsage{Used: &used, Size: &size, Cost: &SessionCost{Amount: .25, Currency: "USD"}}
}

type controlTestAgent struct {
	stubAgent
	workDir       string
	mu            sync.Mutex
	model, effort string
}

func (*controlTestAgent) UsesSessionControls() bool { return true }
func (a *controlTestAgent) GetWorkDir() string      { return a.workDir }
func (a *controlTestAgent) SetSessionConfigDefaults(model, effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	a.effort = effort
}
func (a *controlTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &controlTestSession{cujAgentSession: newCUJAgentSession(), model: a.model, effort: a.effort}, nil
}

func newControlTestEnv(t *testing.T, workspace, stateFile string) *cujEnv {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	a := &controlTestAgent{workDir: workspace, model: "m1", effort: "high"}
	e := NewEngine("controls", a, []Platform{p}, filepath.Join(workspace, "sessions.json"), LangEnglish)
	e.SetProjectStateStore(NewProjectStateStore(stateFile))
	t.Cleanup(func() { _ = e.Stop() })
	return &cujEnv{t: t, engine: e, plat: p}
}

func controlWait(t *testing.T, env *cujEnv, text string) {
	t.Helper()
	env.waitFor(text, 2*time.Second, func() bool { return env.sentContains(text) })
}

// User journey: switch both selectors, converse, inspect usage, restart, and
// converse again. A second workspace must retain its own defaults throughout.
func TestCUJ_A9_SessionControlsPersistPerWorkspace(t *testing.T) {
	root := t.TempDir()
	stateFile := filepath.Join(root, "state.json")
	a := newControlTestEnv(t, filepath.Join(root, "a"), stateFile)
	a.userSends("user", "/model switch 2")
	controlWait(t, a, "m2")
	a.userSends("user", "/reasoning low")
	controlWait(t, a, "`low`")
	a.userSends("user", "hello")
	controlWait(t, a, "m2/low:hello")
	a.userSends("user", "/usage")
	controlWait(t, a, "Remaining: 77%")
	controlWait(t, a, "Remaining: 58%")
	if a.sentContains("42 / 100 tokens") || a.sentContains("0.250000 USD") {
		t.Fatal("context/cost replaced account quota")
	}
	// A normal conversation before and after the commands uses one native ID.
	id := a.engine.sessions.GetOrCreateActive("test:user").GetAgentSessionID()
	if id == "" {
		t.Fatal("commands lost the native session ID")
	}
	b := newControlTestEnv(t, filepath.Join(root, "b"), stateFile)
	b.userSends("user", "hello-b")
	controlWait(t, b, "m1/high:hello-b")
	_ = a.engine.Stop()
	restarted := newControlTestEnv(t, filepath.Join(root, "a"), stateFile)
	restarted.userSends("user", "after-restart")
	controlWait(t, restarted, "m2/low:after-restart")
	if got := restarted.engine.sessions.GetOrCreateActive("test:user").GetAgentSessionID(); got != id {
		t.Fatalf("session ID changed: %q != %q", got, id)
	}
}

type legacyEffortTestAgent struct {
	stubAgent
	workDir, effort string
}

func (a *legacyEffortTestAgent) GetWorkDir() string                { return a.workDir }
func (a *legacyEffortTestAgent) SetReasoningEffort(v string)       { a.effort = v }
func (a *legacyEffortTestAgent) GetReasoningEffort() string        { return a.effort }
func (*legacyEffortTestAgent) AvailableReasoningEfforts() []string { return []string{"low", "high"} }

func TestWorkspaceEffort_PersistsLegacyAgentSelectionWithoutDefaultLeak(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "state.json")
	a := &legacyEffortTestAgent{workDir: filepath.Join(root, "a"), effort: "high"}
	e := NewEngine("legacy", a, nil, "", LangEnglish)
	e.SetProjectStateStore(NewProjectStateStore(file))
	p := &stubPlatformEngine{n: "test"}
	e.cmdReasoning(p, &Message{SessionKey: "test:user", ReplyCtx: "ctx"}, []string{"low"})
	if a.effort != "low" {
		t.Fatal("live setting not applied")
	}
	restart := NewEngine("legacy", a, nil, "", LangEnglish)
	restart.SetProjectStateStore(NewProjectStateStore(file))
	newA := &legacyEffortTestAgent{workDir: a.workDir, effort: "high"}
	restart.restoreWorkspaceEffort(newA, normalizeWorkspacePath(a.workDir))
	if newA.effort != "low" {
		t.Fatal("saved effort not restored")
	}
	newB := &legacyEffortTestAgent{workDir: filepath.Join(root, "b"), effort: "high"}
	restart.restoreWorkspaceEffort(newB, normalizeWorkspacePath(newB.workDir))
	if newB.effort != "high" {
		t.Fatal("effort leaked into another workspace")
	}
}

type noQuotaControlSession struct{ *controlTestSession }

func (*noQuotaControlSession) GetUsage(context.Context) (*UsageReport, error) { return nil, nil }

type noQuotaControlAgent struct{ *controlTestAgent }

func (a *noQuotaControlAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	s, err := a.controlTestAgent.StartSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &noQuotaControlSession{s.(*controlTestSession)}, nil
}

func TestSessionUsageCommand_AccountUnavailableNeverFallsBackToContext(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := &noQuotaControlAgent{&controlTestAgent{workDir: t.TempDir(), model: "m1", effort: "high"}}
	e := NewEngine("quota", a, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	e.cmdUsage(p, &Message{SessionKey: "test:user", ReplyCtx: "ctx"})
	got := p.getSent()
	if len(got) != 1 || got[0] != e.i18n.T(MsgAccountUsageUnavailable) {
		t.Fatalf("missing quota response=%q", got)
	}
}
