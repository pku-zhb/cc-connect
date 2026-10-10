package core

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// SeparatedStreamingCard keeps progress and final answer delivery independent.
// A final-delivery error must not cause the engine to resend an entire answer:
// some chunks may already have been delivered.
type SeparatedStreamingCard interface {
	StreamingCard
	UpdateProgress(context.Context, string) error
	FinalizeAnswer(context.Context, string, string) error
	StopProgress(context.Context, bool)
}

type separatedProgress struct {
	pending strings.Builder
	entries []string
}

func (s *separatedProgress) append(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.entries = append(s.entries, truncateIf(text, 4000))
	for len(s.entries) > 1 && utf8.RuneCountInString(s.render()) > 8000 {
		s.entries = s.entries[1:]
	}
}
func (s *separatedProgress) render() string { return strings.Join(s.entries, "\n\n") }
func (s *separatedProgress) promote(show bool) {
	if show && strings.TrimSpace(s.pending.String()) != "" {
		s.append("💭 " + truncateIf(s.pending.String(), 3500))
	}
	s.pending.Reset()
}
func (s *separatedProgress) observe(event Event, display DisplayCfg) bool {
	switch event.Type {
	case EventHookRejected:
		s.pending.Reset()
	case EventText:
		s.pending.WriteString(event.Content)
	case EventThinking:
		if strings.TrimSpace(event.Content) == "" || isEllipsisOnly(event.Content) {
			return true
		}
		s.promote(display.ThinkingMessages)
		if display.ThinkingMessages {
			limit := 3500
			if display.ThinkingMaxLen > 0 && display.ThinkingMaxLen < limit {
				limit = display.ThinkingMaxLen
			}
			s.append("💭 " + truncateIf(event.Content, limit))
		}
		return true
	case EventToolUse:
		s.promote(display.ThinkingMessages)
		if display.ToolMessages {
			s.append(fmt.Sprintf("🔧 **%s**\n%s", event.ToolName, truncateIf(event.ToolInput, display.ToolMaxLen)))
		}
		return true
	case EventToolResult:
		if display.ToolMessages {
			text := event.ToolResult
			if text == "" {
				text = event.Content
			}
			if text != "" {
				s.append(fmt.Sprintf("🔧 **%s**\n%s", event.ToolName, truncateIf(text, display.ToolMaxLen)))
			}
		}
		return true
	case EventPermissionRequest:
		s.promote(display.ThinkingMessages)
	}
	return false
}
func (s *separatedProgress) answer(result string) string {
	if strings.TrimSpace(result) != "" {
		return result
	}
	return s.pending.String()
}
