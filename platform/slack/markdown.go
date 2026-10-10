package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
	"github.com/slack-go/slack"
)

const slackMarkdownMaxChars = 12000

// markdownBlock implements the wire format without upgrading the Slack SDK.
// Slack translates this block server-side; it is distinct from mrkdwn text.
type markdownBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (b markdownBlock) BlockType() slack.MessageBlockType { return slack.MessageBlockType("markdown") }
func (b markdownBlock) ID() string                        { return "" }

type messageRenderer struct {
	mode     string
	rejected atomic.Bool
}

func newMessageRenderer(raw any) (*messageRenderer, error) {
	mode := "auto"
	if raw != nil {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("slack: markdown_mode must be auto, native, or mrkdwn")
		}
		mode = strings.ToLower(strings.TrimSpace(s))
	}
	switch mode {
	case "auto", "native", "mrkdwn":
		return &messageRenderer{mode: mode}, nil
	default:
		return nil, fmt.Errorf("slack: invalid markdown_mode %q (want auto, native, or mrkdwn)", mode)
	}
}
func (r *messageRenderer) native() bool {
	return r != nil && r.mode != "mrkdwn" && !r.rejected.Load()
}
func (r *messageRenderer) options(content string, update bool) []slack.MsgOption {
	if r.native() {
		// Top-level text is an accessible/notification fallback, not the displayed
		// body. Disable mrkdwn there so approximation signs cannot become strikes.
		fallback := core.StripMarkdown(content)
		// chat.update's top-level text has a byte cap even when the displayed
		// Markdown block is within its separate 12,000-character limit.
		if len(fallback) > slackUpdateMaxText {
			end := slackUpdateMaxText
			for !utf8.RuneStart(fallback[end]) {
				end--
			}
			fallback = fallback[:end]
		}
		return []slack.MsgOption{slack.MsgOptionText(fallback, false), slack.MsgOptionDisableMarkdown(),
			slack.MsgOptionBlocks(markdownBlock{Type: "markdown", Text: content})}
	}
	opts := []slack.MsgOption{slack.MsgOptionText(core.MarkdownToSlackMrkdwn(content), false)}
	if update {
		// An update without blocks retains the old blocks. Explicitly clear them
		// when falling back from a previously successful native message.
		opts = append(opts, slack.MsgOptionBlocks([]slack.Block{}...))
	}
	return opts
}
func (r *messageRenderer) fallback(err error) bool {
	if r == nil || r.mode != "auto" {
		return false
	}
	var apiErr slack.SlackErrorResponse
	if !errors.As(err, &apiErr) {
		return false
	}
	// Retry only explicit block rejections. Timeouts/auth/rate limits must not
	// trigger a second post whose original delivery status is unknown.
	switch apiErr.Err {
	case "invalid_blocks", "invalid_block_type", "unsupported_block_type":
		if r.rejected.CompareAndSwap(false, true) {
			slog.Warn("slack: native Markdown rejected; using mrkdwn until restart", "code", apiErr.Err)
		}
		return true
	default:
		return false
	}
}
func (r *messageRenderer) post(ctx context.Context, client *slack.Client, channel, thread, content string) (string, error) {
	send := func() (string, error) {
		opts := r.options(content, false)
		if thread != "" {
			opts = append(opts, slack.MsgOptionPostMessageParameters(slack.PostMessageParameters{ThreadTimestamp: thread}))
		}
		_, ts, err := client.PostMessageContext(ctx, channel, opts...)
		return ts, err
	}
	ts, err := send()
	if r.fallback(err) {
		return send()
	}
	return ts, err
}
func (r *messageRenderer) update(ctx context.Context, client *slack.Client, channel, ts, content string) error {
	send := func() error {
		_, _, _, err := client.UpdateMessageContext(ctx, channel, ts, r.options(content, true)...)
		return err
	}
	err := send()
	if r.fallback(err) {
		if !r.fitsUpdate(content) {
			return fmt.Errorf("slack: fallback update exceeds legacy text limit")
		}
		return send()
	}
	return err
}
func (r *messageRenderer) fitsUpdate(content string) bool {
	if r.native() {
		return utf8.RuneCountInString(content) <= slackMarkdownMaxChars
	}
	return len(core.MarkdownToSlackMrkdwn(content)) <= slackUpdateMaxText
}
func (r *messageRenderer) chunks(content string) ([]string, error) {
	if !r.native() {
		return core.SplitMessageCodeFenceAware(content, slackMarkdownMaxChars), nil
	}
	return splitSlackMarkdown(content, slackMarkdownMaxChars)
}
func (r *messageRenderer) postAll(ctx context.Context, client *slack.Client, channel, thread, content string) (string, error) {
	chunks, err := r.chunks(content)
	if err != nil {
		return "", err
	}
	var firstTS string
	for _, chunk := range chunks {
		ts, err := r.post(ctx, client, channel, thread, chunk)
		if err != nil {
			return firstTS, err
		}
		if firstTS == "" {
			firstTS = ts
		}
	}
	return firstTS, nil
}

var tableSeparator = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$`)

// splitSlackMarkdown keeps normal tables and fenced code intact. Oversized
// tables continue with their header; oversized code continues in a new fence.
// A single table row too large for a block returns an error instead of silently
// corrupting its columns or dropping data.
func splitSlackMarkdown(content string, limit int) ([]string, error) {
	if limit < 1 {
		return nil, fmt.Errorf("slack: Markdown block limit must be positive")
	}
	if utf8.RuneCountInString(content) <= limit {
		return []string{content}, nil
	}
	lines := strings.SplitAfter(content, "\n")
	var parts []string
	for i := 0; i < len(lines); {
		start := i
		trimmed := strings.TrimSpace(lines[i])
		fence := ""
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			for n := 3; n < len(trimmed) && trimmed[n] == fence[0]; n++ {
				fence += string(fence[0])
			}
			i++
			for i < len(lines) {
				close := strings.TrimSpace(lines[i])
				i++
				if strings.Trim(close, string(fence[0])) == "" && len(close) >= len(fence) {
					break
				}
			}
		} else if i+1 < len(lines) && strings.Contains(lines[i], "|") && tableSeparator.MatchString(strings.TrimSpace(lines[i+1])) {
			i += 2
			for i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != "" {
				i++
			}
			header := lines[start] + lines[start+1]
			table := strings.Join(lines[start:i], "")
			if utf8.RuneCountInString(table) <= limit {
				parts = append(parts, table)
				continue
			}
			if utf8.RuneCountInString(header) > limit {
				return nil, fmt.Errorf("slack: Markdown table header exceeds %d characters", limit)
			}
			current := header
			for _, row := range lines[start+2 : i] {
				if utf8.RuneCountInString(header+row) > limit {
					return nil, fmt.Errorf("slack: Markdown table row exceeds %d characters; send the table as a file", limit)
				}
				if utf8.RuneCountInString(current+row) > limit {
					parts = append(parts, current)
					current = header
				}
				current += row
			}
			parts = append(parts, current)
			continue
		} else {
			i++
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") && !strings.HasPrefix(strings.TrimSpace(lines[i]), "~~~") {
				if i+1 < len(lines) && strings.Contains(lines[i], "|") && tableSeparator.MatchString(strings.TrimSpace(lines[i+1])) {
					break
				}
				i++
			}
		}
		block := strings.Join(lines[start:i], "")
		if utf8.RuneCountInString(block) <= limit {
			parts = append(parts, block)
			continue
		}
		if fence != "" {
			opener := strings.TrimRight(lines[start], "\r\n") + "\n"
			end := i
			if close := strings.TrimSpace(lines[i-1]); strings.Trim(close, string(fence[0])) == "" && len(close) >= len(fence) {
				end--
			}
			body := strings.Join(lines[start+1:end], "")
			available := limit - utf8.RuneCountInString(opener+"\n"+fence+"\n")
			if available < 1 {
				return nil, fmt.Errorf("slack: Markdown code fence header exceeds block limit")
			}
			for _, chunk := range splitPlainRunes(body, available) {
				parts = append(parts, opener+chunk+"\n"+fence+"\n")
			}
		} else {
			parts = append(parts, splitPlainRunes(block, limit)...)
		}
	}
	var chunks []string
	current := ""
	for _, part := range parts {
		if utf8.RuneCountInString(current+part) > limit {
			chunks = append(chunks, current)
			current = ""
		}
		current += part
	}
	if current != "" {
		chunks = append(chunks, current)
	}
	return chunks, nil
}
func splitPlainRunes(text string, limit int) []string {
	runes := []rune(text)
	var chunks []string
	for len(runes) > limit {
		n := limit
		// Prefer a newline while avoiding tiny chunks from a distant separator.
		for i := limit - 1; i >= limit/2; i-- {
			if runes[i] == '\n' {
				n = i + 1
				break
			}
		}
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	if len(runes) > 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks
}
