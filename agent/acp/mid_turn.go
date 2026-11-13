package acp

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// Mid-turn input modes decide what /ps and /btw do while a turn is running.
const (
	// midTurnQueue: the engine queues the input as the next turn.
	midTurnQueue = ""
	// midTurnPrompt: the agent folds a concurrent session/prompt into the
	// running turn at its next tool boundary (Antigravity agy acp).
	midTurnPrompt = "prompt"
	// midTurnInterrupt: wait for the running tool call to finish, cancel the
	// turn, then prompt again with the interjection so work continues.
	midTurnInterrupt = "interrupt"
)

func normalizeMidTurnInput(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "queue":
		return midTurnQueue, nil
	case "prompt", "native":
		return midTurnPrompt, nil
	case "interrupt":
		return midTurnInterrupt, nil
	}
	return "", fmt.Errorf("acp: unsupported mid_turn_input %q (want queue, prompt or interrupt)", v)
}

// AcceptsMidTurnInput reports whether /ps text can reach the running turn.
// Without a mid-turn mode, session/prompt blocks per turn and the engine
// queues the input as the next turn.
func (s *acpSession) AcceptsMidTurnInput() bool { return s.midTurnInput != midTurnQueue }

// beginPrompt claims a prompt slot in the current logical turn and reports
// whether another prompt was already running.
func (s *acpSession) beginPrompt() (busy bool) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	busy = s.outstanding > 0
	s.outstanding++
	return busy
}

// finishPrompt releases a prompt slot and reports whether this prompt ended
// the logical turn. An interjected turn spans several session/prompt calls;
// only the last one to settle may emit the turn's result.
func (s *acpSession) finishPrompt() bool {
	if s.midTurnInput == midTurnQueue {
		return true
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	s.outstanding--
	if s.outstanding > 0 {
		return false
	}
	s.outstanding = 0
	clear(s.activeTools)
	s.turnCond.Broadcast()
	return true
}

// trackToolActivity records which tool calls are still running so an
// interrupting interjection can wait for the current one to finish.
func (s *acpSession) trackToolActivity(params json.RawMessage) {
	if s.midTurnInput != midTurnInterrupt {
		return
	}
	var wrap struct {
		Update struct {
			Kind   string `json:"sessionUpdate"`
			ID     string `json:"toolCallId"`
			Status string `json:"status"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || wrap.Update.ID == "" {
		return
	}
	if wrap.Update.Kind != "tool_call" && wrap.Update.Kind != "tool_call_update" {
		return
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	switch wrap.Update.Status {
	case "completed", "failed", "cancelled", "canceled":
		delete(s.activeTools, wrap.Update.ID)
		s.turnCond.Broadcast()
	default:
		if wrap.Update.Kind == "tool_call" || wrap.Update.Status == "in_progress" || wrap.Update.Status == "pending" {
			s.activeTools[wrap.Update.ID] = struct{}{}
		}
	}
}

// waitToolsIdle blocks until no tool call is running, the turn ends, or the
// session closes.
func (s *acpSession) waitToolsIdle() {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	for len(s.activeTools) > 0 && s.outstanding > 0 && s.ctx.Err() == nil {
		s.turnCond.Wait()
	}
}

func interjectionPrompt(text string) string {
	return "[User interjection while you were working. Your previous step was interrupted only to deliver this message.]\n\n" +
		text +
		"\n\n[Take this into account and continue the original task from where you left off. Do not redo work that is already done.]"
}

// interject delivers mid-turn input. The caller already claimed a prompt
// slot, so the running prompt settling cannot end the logical turn.
func (s *acpSession) interject(text string) {
	switch s.midTurnInput {
	case midTurnPrompt:
		slog.Info("acp: delivering mid-turn input into the running turn", "session_id", s.currentACPSessionID())
		_ = s.runPrompt(text)
	case midTurnInterrupt:
		s.interjectMu.Lock()
		s.waitToolsIdle()
		slog.Info("acp: interrupting the turn to deliver mid-turn input", "session_id", s.currentACPSessionID())
		if err := s.CancelTurn(); err != nil {
			slog.Warn("acp: cancel before interjection failed", "error", err)
		}
		s.interjectMu.Unlock()
		// The cancelled prompt holds sendMu until it settles; one prompt
		// runs at a time on the conversation.
		s.sendMu.Lock()
		defer s.sendMu.Unlock()
		_ = s.runPrompt(interjectionPrompt(text))
	}
}
