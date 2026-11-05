package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fastTestAgent struct {
	stubAgent
	mu                  sync.Mutex
	name, workDir, tier string
	readErr             error
	live                []*fastTestSession
}

func (a *fastTestAgent) Name() string {
	if a.name != "" {
		return a.name
	}
	return "fast-test"
}
func (a *fastTestAgent) GetWorkDir() string         { return a.workDir }
func (a *fastTestAgent) SetServiceTier(tier string) { a.mu.Lock(); defer a.mu.Unlock(); a.tier = tier }
func (a *fastTestAgent) GetServiceTier(context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tier != "" {
		return a.tier, nil
	}
	if a.readErr != nil {
		return "", a.readErr
	}
	return "priority", nil // inherited native Fast default
}
func (a *fastTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := &fastTestSession{cujAgentSession: newCUJAgentSession(), owner: a, started: make(chan string, 8)}
	a.live = append(a.live, s)
	return s, nil
}
func (a *fastTestAgent) session() *fastTestSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.live[len(a.live)-1]
}

type fastTestSession struct {
	*cujAgentSession
	owner   *fastTestAgent
	started chan string
}

func (s *fastTestSession) GetServiceTier(ctx context.Context) (string, error) {
	return s.owner.GetServiceTier(ctx)
}
func (s *fastTestSession) Send(prompt, id string, images []ImageAttachment, files []FileAttachment) error {
	tier, _ := s.owner.GetServiceTier(context.Background())
	s.started <- tier + ":" + prompt
	if prompt == "held" {
		return nil
	}
	s.mu.Lock()
	s.reply = tier + ":" + prompt
	s.mu.Unlock()
	return s.cujAgentSession.Send(prompt, id, images, files)
}
func newFastTestEnv(t *testing.T, workspace, stateFile string) *cujEnv {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	a := &fastTestAgent{workDir: workspace}
	e := NewEngine("fast", a, []Platform{p}, filepath.Join(workspace, "sessions.json"), LangEnglish)
	e.SetProjectStateStore(NewProjectStateStore(stateFile))
	t.Cleanup(func() { _ = e.Stop() })
	return &cujEnv{t: t, engine: e, plat: p}
}

func TestFast_InvalidUnsupportedAndFailedReadNeverReachAgent(t *testing.T) {
	for _, tc := range []struct {
		name, command         string
		unsupported, readFail bool
	}{
		{name: "invalid", command: "/fast maybe"},
		{name: "extra", command: "/fast on extra"},
		{name: "unsupported", command: "/fast on", unsupported: true},
		{name: "read-failed", command: "/fast", readFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			a := &fastTestAgent{workDir: t.TempDir()}
			var ag Agent = a
			if tc.unsupported {
				ag = &stubAgent{}
			}
			if tc.readFail {
				a.readErr = errors.New("probe failed")
			}
			e := NewEngine("fast", ag, []Platform{p}, "", LangEnglish)
			defer e.Stop()
			if !e.handleCommand(p, &Message{SessionKey: "test:u", ReplyCtx: "ctx"}, tc.command) {
				t.Fatal("command fell through to agent")
			}
			if len(p.getSent()) != 1 {
				t.Fatalf("responses = %v", p.getSent())
			}
			if a.tier != "" || len(a.live) != 0 {
				t.Fatal("invalid command changed state or started agent")
			}
		})
	}
}

func TestFast_MultiWorkspaceRecreationUsesSameScopeAsModel(t *testing.T) {
	name := "test-fast-workspace-factory"
	RegisterAgent(name, func(opts map[string]any) (Agent, error) {
		dir, _ := opts["work_dir"].(string)
		return &fastTestAgent{name: name, workDir: dir}, nil
	})
	p := &stubPlatformEngine{n: "plain"}
	global := &fastTestAgent{name: name, workDir: t.TempDir()}
	file := filepath.Join(t.TempDir(), "state.json")
	e := NewEngine("fast", global, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	e.SetProjectStateStore(NewProjectStateStore(file))
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))
	workspace := normalizeWorkspacePath(t.TempDir())
	e.workspaceBindings.Bind("project:fast", "C-fast", "channel", workspace)
	e.cmdFast(p, &Message{SessionKey: "feishu:C-fast:u1", ReplyCtx: "ctx"}, []string{"off"})
	if got := NewProjectStateStore(file).WorkspaceServiceTierOverride(workspace); got != "default" {
		t.Fatalf("saved %q", got)
	}
	if global.tier != "" {
		t.Fatal("workspace selection leaked into root agent")
	}
	ws := e.workspacePool.GetOrCreate(workspace)
	ws.mu.Lock()
	ws.agent = nil
	ws.sessions = nil
	ws.mu.Unlock()
	recreated, _, err := e.getOrCreateWorkspaceAgent(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := recreated.(ServiceTierSwitcher).GetServiceTier(context.Background()); got != "default" {
		t.Fatalf("recreated = %q", got)
	}
	other, _, err := e.getOrCreateWorkspaceAgent(normalizeWorkspacePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := other.(ServiceTierSwitcher).GetServiceTier(context.Background()); got != "priority" {
		t.Fatalf("other = %q", got)
	}
}

func TestFast_ExplicitOnDoesNotRequireReadingNativeDefault(t *testing.T) {
	env := newFastTestEnv(t, t.TempDir(), filepath.Join(t.TempDir(), "state.json"))
	a := env.engine.agent.(*fastTestAgent)
	a.readErr = errors.New("offline")
	env.userSends("u", "/fast on")
	if !strings.Contains(strings.Join(env.plat.getSent(), "\n"), "`on`") {
		t.Fatal(env.plat.getSent())
	}
}
