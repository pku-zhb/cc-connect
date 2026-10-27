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
	pending       strings.Builder
	entries       []string
	thinkingDelta bool
	thinkingID    string
	thinkingText  string
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

// Summary parts arrive separated by blank lines, sometimes across ACP chunks.
// Render markers from the accumulated raw text so chunk boundaries cannot add
// duplicate markers or leave a later summary without its thinking marker.
func renderThoughtParagraphs(text string) string {
	parts := strings.Split(text, "\n\n")
	for i, part := range parts {
		if strings.TrimSpace(part) != "" {
			parts[i] = "💭 " + part
		}
	}
	return strings.Join(parts, "\n\n")
}

func (s *separatedProgress) observe(event Event, display DisplayCfg) bool {
	if event.Type != EventThinking {
		s.thinkingDelta = false
	}
	switch event.Type {
	case EventHookRejected:
		s.pending.Reset()
	case EventText:
		s.pending.WriteString(event.Content)
	case EventThinking:
		delta, _ := event.Metadata["thinking_delta"].(bool)
		id, _ := event.Metadata["thinking_id"].(string)
		continuation := delta && s.thinkingDelta && id == s.thinkingID && len(s.entries) > 0
		// Whitespace can separate words or paragraphs in a thought delta.
		if (!continuation && strings.TrimSpace(event.Content) == "") || event.Content == "" || (!delta && isEllipsisOnly(event.Content)) {
			return true
		}
		s.promote(display.ThinkingMessages)
		if display.ThinkingMessages {
			limit := 3500
			if display.ThinkingMaxLen > 0 && display.ThinkingMaxLen < limit {
				limit = display.ThinkingMaxLen
			}
			text := truncateIf(event.Content, limit)
			if delta {
				if continuation {
					text = s.thinkingText + text
				}
				s.thinkingText = truncateIf(text, 4000)
				text = renderThoughtParagraphs(s.thinkingText)
			} else {
				text = "💭 " + text
			}
			if continuation {
				last := len(s.entries) - 1
				s.entries[last] = truncateIf(text, 4000)
				for len(s.entries) > 1 && utf8.RuneCountInString(s.render()) > 8000 {
					s.entries = s.entries[1:]
				}
			} else {
				s.append(text)
			}
		}
		s.thinkingDelta = delta && display.ThinkingMessages
		s.thinkingID = id
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
