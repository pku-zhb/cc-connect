package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

type lifecycleReceiptPlatform struct {
	stubPlatformEngine
	eyes map[string]bool
}

func (p *lifecycleReceiptPlatform) acknowledge(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eyes[id] = true
	p.sent = append(p.sent, "eyes:"+id)
}
func (p *lifecycleReceiptPlatform) FinishReceipt(_ context.Context, ctx any) {
	id, _ := ctx.(string)
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.eyes, id)
	p.sent = append(p.sent, "cleared:"+id)
}
func (p *lifecycleReceiptPlatform) hasReceipt(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.eyes[id]
}

func newLifecycleReceiptEnv(t *testing.T, startErr error) (*cujEnv, *receiptStartAgent, *lifecycleReceiptPlatform) {
	t.Helper()
	p := &lifecycleReceiptPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, eyes: make(map[string]bool)}
	a := &receiptStartAgent{session: &receiptAgentSession{queuingAgentSession: newQueuingSession("receipt-lifecycle")}, entered: make(chan struct{}), release: make(chan struct{}), err: startErr}
	e := NewEngine("receipt-lifecycle", a, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	return &cujEnv{t: t, engine: e, plat: &p.stubPlatformEngine}, a, p
}

func sendLifecycleMessage(e *Engine, p *lifecycleReceiptPlatform, id, text string) {
	e.ReceiveMessage(p, &Message{SessionKey: "test:lifecycle", Platform: "test", UserID: "owner", MessageID: id, Content: text, ReplyCtx: id,
		OnAccepted: func() { p.acknowledge(id) },
	})
}

func TestReceiptCleanup_OnAgentStartupFailure(t *testing.T) {
	env, agent, p := newLifecycleReceiptEnv(t, errors.New("fixture startup failure"))
	sendLifecycleMessage(env.engine, p, "first", "hello")
	select {
	case <-agent.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("startup did not begin")
	}
	if !p.hasReceipt("first") {
		t.Fatal("receipt must be visible during startup")
	}
	close(agent.release)
	env.waitFor("failure and receipt cleanup", 2*time.Second, func() bool { return env.sentContains("failed to start agent session") && !p.hasReceipt("first") })
}
