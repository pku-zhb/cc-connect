package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSeparatedProgress_VisibilityAndFinalBoundary(t *testing.T) {
	for _, thinking := range []bool{false, true} {
		for _, tools := range []bool{false, true} {
			t.Run(fmt.Sprintf("thinking=%v/tools=%v", thinking, tools), func(t *testing.T) {
				var s separatedProgress
				display := DisplayCfg{ThinkingMessages: thinking, ToolMessages: tools}
				for _, e := range []Event{{Type: EventText, Content: "Investigating"}, {Type: EventToolUse, ToolName: "Search", ToolInput: "query"}, {Type: EventToolResult, ToolName: "Search", ToolResult: "Found"}, {Type: EventThinking, Content: "Checking"}, {Type: EventText, Content: "Final "}, {Type: EventText, Content: "answer"}} {
					s.observe(e, display)
				}
				if got := s.answer(""); got != "Final answer" {
					t.Fatalf("final contaminated: %q", got)
				}
				if s.answer("Explicit result") != "Explicit result" {
					t.Fatal("result lost priority")
				}
				p := s.render()
				if strings.Contains(p, "Investigating") != thinking || strings.Contains(p, "Checking") != thinking || strings.Contains(p, "Search") != tools || strings.Contains(p, "Found") != tools || strings.Contains(p, "Final answer") {
					t.Fatalf("visibility mismatch: %q", p)
				}
			})
		}
	}
}

type separatedRecordingCard struct {
	recordingStreamCard
	progress, answer string
	fail             bool
}

func (c *separatedRecordingCard) UpdateProgress(_ context.Context, p string) error {
	c.progress = p
	return nil
}
func (c *separatedRecordingCard) FinalizeAnswer(_ context.Context, p, a string) error {
	c.progress = p
	c.answer = a
	if c.fail {
		return errors.New("delivery failed")
	}
	return nil
}
func (c *separatedRecordingCard) StopProgress(context.Context, bool) {}

type separatedRecordingPlatform struct {
	stubPlatformEngine
	card *separatedRecordingCard
}

func (p *separatedRecordingPlatform) CreateStreamingCard(context.Context, any) (StreamingCard, error) {
	return p.card, nil
}

func TestSeparatedDelivery_EngineSendsFinalWithoutProgressContamination(t *testing.T) {
	for _, result := range []string{"", "Authoritative answer", "NO_REPLY"} {
		t.Run(result, func(t *testing.T) {
			card := &separatedRecordingCard{}
			p := &separatedRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "separated"}, card: card}
			agent := &stubAgent{}
			e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
			e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ThinkingMessages: true, ToolMessages: true})
			session := e.sessions.GetOrCreateActive("test:thread")
			events := make(chan Event, 8)
			as := newControllableSession("test-agent")
			events = as.events
			state := &interactiveState{agentSession: as, agent: agent, platform: p, replyCtx: "ctx"}
			e.interactiveStates["test:thread"] = state
			events <- Event{Type: EventText, Content: "Investigating"}
			events <- Event{Type: EventToolUse, ToolName: "Search", ToolInput: "query"}
			events <- Event{Type: EventToolResult, ToolName: "Search", ToolResult: "Found"}
			events <- Event{Type: EventText, Content: "Final answer"}
			events <- Event{Type: EventResult, Content: result, Done: true}
			e.processInteractiveEvents(state, session, e.sessions, "test:thread", "msg", time.Now(), nil, nil, "ctx", 0)
			want := result
			if result == "" {
				want = "Final answer"
			}
			if result == "NO_REPLY" {
				want = ""
			}
			if card.answer != want {
				t.Fatalf("answer=%q want=%q", card.answer, want)
			}
			if !strings.Contains(card.progress, "Investigating") || !strings.Contains(card.progress, "Found") || strings.Contains(card.progress, "Final answer") {
				t.Fatalf("bad progress %q", card.progress)
			}
		})
	}
}

func TestSeparatedProgress_EmptyPendingTextDoesNotCreateThinkingEmoji(t *testing.T) {
	var progress separatedProgress
	display := DisplayCfg{ThinkingMessages: true}
	for _, event := range []Event{{Type: EventThinking, Content: "Checking"}, {Type: EventToolUse, ToolName: "Search"}, {Type: EventText, Content: " \n "}, {Type: EventToolUse, ToolName: "Search"}} {
		progress.observe(event, display)
	}
	if progress.render() != "💭 Checking" {
		t.Fatalf("empty thinking entries leaked: %q", progress.render())
	}
}

func TestSeparatedProgress_MuseThoughtDeltasContinueWithoutPollutingFinal(t *testing.T) {
	for _, visible := range []bool{true, false} {
		var s separatedProgress
		display := DisplayCfg{ThinkingMessages: visible}
		for _, text := range []string{"Quantifying", " ", "per-core bandwidth.", "\n\n", "核对延迟。"} {
			s.observe(Event{Type: EventThinking, Content: text, Metadata: map[string]any{"thinking_delta": true, "thinking_id": "thought-1"}}, display)
		}
		s.observe(Event{Type: EventText, Content: "先给一个结论。"}, display)
		if s.answer("") != "先给一个结论。" {
			t.Fatalf("final contaminated: %q", s.answer(""))
		}
		want := ""
		if visible {
			want = "💭 Quantifying per-core bandwidth.\n\n💭 核对延迟。"
		}
		if got := s.render(); got != want {
			t.Fatalf("visible=%v progress=%q want=%q", visible, got, want)
		}
	}
}

func TestSeparatedProgress_ThoughtDeltaBoundariesPreserveOtherAgents(t *testing.T) {
	var s separatedProgress
	display := DisplayCfg{ThinkingMessages: true}
	thought := func(id, text string) Event {
		return Event{Type: EventThinking, Content: text, Metadata: map[string]any{"thinking_delta": true, "thinking_id": id}}
	}
	for _, event := range []Event{
		thought("one", "First"), thought("one", " thought."),
		thought("two", "Second thought."),
		{Type: EventToolUse, ToolName: "Search"},
		thought("", "After tool."),
		{Type: EventThinking, Content: "Codex or OpenCode summary."},
		{Type: EventText, Content: "Final answer."},
	} {
		s.observe(event, display)
	}
	want := "💭 First thought.\n\n💭 Second thought.\n\n💭 After tool.\n\n💭 Codex or OpenCode summary."
	if s.render() != want || s.answer("") != "Final answer." {
		t.Fatalf("progress=%q final=%q", s.render(), s.answer(""))
	}
}

func TestSeparatedProgress_ThoughtParagraphEmojiIsIndependentOfChunkBoundaries(t *testing.T) {
	for _, parts := range [][]string{
		{"First summary.\n\nSecond summary."},
		{"First", " summary.", "\n\nSecond", " summary."},
		{"First summary.", "\n", "\n", "Second summary."},
		{"First summary.", "\n\n", "Second summary.", "\n\n"},
	} {
		var s separatedProgress
		for _, part := range parts {
			s.observe(Event{Type: EventThinking, Content: part, Metadata: map[string]any{"thinking_delta": true}}, DisplayCfg{ThinkingMessages: true})
		}
		want := "💭 First summary.\n\n💭 Second summary."
		if got := strings.TrimRight(s.render(), "\n"); got != want {
			t.Fatalf("chunks=%q progress=%q want=%q", parts, got, want)
		}
		if strings.Count(s.render(), "💭") != 2 {
			t.Fatalf("empty or duplicate marker for chunks=%q: %q", parts, s.render())
		}
	}
}
