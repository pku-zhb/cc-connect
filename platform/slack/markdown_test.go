package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/slack-go/slack"
)

type capturedMessage struct{ path, text, blocks, thread, mrkdwn string }

func markdownTestClient(t *testing.T, respond func(int, capturedMessage) string) (*slack.Client, *[]capturedMessage) {
	t.Helper()
	calls := new([]capturedMessage)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Error(err)
		}
		m := capturedMessage{req.URL.Path, req.FormValue("text"), req.FormValue("blocks"), req.FormValue("thread_ts"), req.FormValue("mrkdwn")}
		*calls = append(*calls, m)
		result := `{"ok":true,"channel":"C1","ts":"1.2"}`
		if respond != nil {
			result = respond(len(*calls), m)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(result))
	}))
	t.Cleanup(srv.Close)
	return slack.New("test", slack.OptionAPIURL(srv.URL+"/")), calls
}
func assertNative(t *testing.T, m capturedMessage, want string) {
	t.Helper()
	var blocks []markdownBlock
	if err := json.Unmarshal([]byte(m.blocks), &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Type != "markdown" || blocks[0].Text != want {
		t.Fatalf("native block = %#v, want %q", blocks, want)
	}
	if m.mrkdwn != "false" {
		t.Fatal("accessible fallback must disable mrkdwn")
	}
}
func TestNativeMarkdown_AllReplyPathsPreserveTablesAndApproximationSigns(t *testing.T) {
	content := "## Revenue\n\n**Growth** ~56%\n\n| Year | USD |\n| --- | --- |\n| FY28 | 20B |\n\n[Source](https://example.com)"
	client, calls := markdownTestClient(t, nil)
	renderer, _ := newMessageRenderer(nil)
	p := &Platform{client: client, renderer: renderer}
	ctx := context.Background()
	rc := replyContext{channel: "C1", timestamp: "1.0"}
	if err := p.Reply(ctx, rc, content); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(ctx, rc, content); err != nil {
		t.Fatal(err)
	}
	handle, err := p.SendPreviewStart(ctx, rc, content)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateMessage(ctx, handle, content); err != nil {
		t.Fatal(err)
	}
	card, err := p.CreateStreamingCard(ctx, rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := card.Update(ctx, content); err != nil {
		t.Fatal(err)
	}
	if err := card.Finalize(ctx, content+"\nDone"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 6 {
		t.Fatalf("calls = %d", len(*calls))
	}
	for i, m := range *calls {
		want := content
		if i == 5 {
			want += "\nDone"
		}
		assertNative(t, m, want)
		if m.path == "/chat.postMessage" && m.thread != "1.0" {
			t.Fatal("lost thread")
		}
	}
	if !strings.Contains(p.FormattingInstructions(), "standard Markdown") {
		t.Fatal("agent prompt still requests mrkdwn")
	}
}
func TestNativeMarkdown_ExplicitBlockRejectionFallsBackAndClearsBlocks(t *testing.T) {
	client, calls := markdownTestClient(t, func(n int, m capturedMessage) string {
		if n == 2 {
			return `{"ok":false,"error":"invalid_blocks"}`
		}
		return `{"ok":true,"channel":"C1","ts":"1.2"}`
	})
	renderer, _ := newMessageRenderer("auto")
	p := &Platform{client: client, renderer: renderer}
	ctx := context.Background()
	h, err := p.SendPreviewStart(ctx, replyContext{channel: "C1", timestamp: "1.0"}, "**first**")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateMessage(ctx, h, "**final**"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 {
		t.Fatalf("calls=%d", len(*calls))
	}
	if (*calls)[2].blocks != "[]" || (*calls)[2].text != "*final*" {
		t.Fatalf("fallback left stale blocks: %#v", (*calls)[2])
	}
	if err := p.Send(ctx, replyContext{channel: "C1"}, "**next**"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[3].blocks != "" {
		t.Fatal("re-probed rejected native blocks")
	}
}
func TestNativeMarkdown_DoesNotRetryOtherErrorsOrStrictMode(t *testing.T) {
	for _, tc := range []struct{ mode, code string }{{"auto", "ratelimited"}, {"auto", "not_in_channel"}, {"auto", "msg_too_long"}, {"native", "invalid_blocks"}} {
		t.Run(tc.mode+tc.code, func(t *testing.T) {
			client, calls := markdownTestClient(t, func(int, capturedMessage) string { return `{"ok":false,"error":"` + tc.code + `"}` })
			renderer, _ := newMessageRenderer(tc.mode)
			_, err := renderer.post(context.Background(), client, "C1", "", "hi")
			if err == nil || len(*calls) != 1 {
				t.Fatalf("err=%v calls=%d", err, len(*calls))
			}
		})
	}
}
func TestNativeMarkdown_ConfigAndLegacy(t *testing.T) {
	for _, raw := range []any{"bad", true, ""} {
		if _, err := newMessageRenderer(raw); err == nil {
			t.Fatalf("accepted %v", raw)
		}
	}
	renderer, _ := newMessageRenderer("mrkdwn")
	client, calls := markdownTestClient(t, nil)
	p := &Platform{client: client, renderer: renderer}
	if err := p.Reply(context.Background(), replyContext{channel: "C1"}, "**bold**"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].blocks != "" || (*calls)[0].text != "*bold*" {
		t.Fatalf("legacy changed: %#v", (*calls)[0])
	}
	if !strings.Contains(p.FormattingInstructions(), "mrkdwn format") {
		t.Fatal("legacy prompt changed")
	}
}
func TestSplitSlackMarkdown_TablesFencesUnicodeAndNoTruncation(t *testing.T) {
	prefix := strings.Repeat("前", 100) + "\n\n"
	table := "| 年 | 值 |\n| --- | --- |\n" + strings.Repeat("| 2028 | 20B |\n", 30)
	for _, fence := range []string{"```python", "~~~~python"} {
		content := prefix + table + "\n" + fence + "\n" + strings.Repeat("print('中')\n", 40) + strings.Repeat(fence[:1], len(fence)-len("python")) + "\nTail"
		chunks, err := splitSlackMarkdown(content, 160)
		if err != nil {
			t.Fatal(err)
		}
		var all strings.Builder
		for _, c := range chunks {
			if c == "" || !utf8.ValidString(c) || utf8.RuneCountInString(c) > 160 {
				t.Fatalf("invalid chunk %q", c)
			}
			if strings.Contains(c, "| 2028") && !strings.Contains(c, "| --- | --- |") {
				t.Fatalf("table lost header: %q", c)
			}
			if strings.Contains(c, "print(") && !strings.Contains(c, fence) {
				t.Fatalf("code lost fence: %q", c)
			}
			all.WriteString(c)
		}
		if strings.Count(all.String(), "| 2028 |") != 30 || strings.Count(all.String(), "print('中')") != 40 || !strings.HasSuffix(all.String(), "Tail") {
			t.Fatal("content lost")
		}
	}
	// Unsplittable rows must be rejected before any API request.
	if _, err := splitSlackMarkdown("| A | B |\n| --- | --- |\n| "+strings.Repeat("x", 200)+" | z |", 100); err == nil {
		t.Fatal("oversize table row silently corrupted")
	}
}
func TestNativeMarkdown_LongCardUsesBlocksBeyondLegacyLimitAndSplitsFinal(t *testing.T) {
	client, calls := markdownTestClient(t, nil)
	renderer, _ := newMessageRenderer("native")
	card := &slackStreamingCard{client: client, renderer: renderer, channel: "C1", threadTS: "1.0"}
	ctx := context.Background()
	if err := card.Update(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	medium := strings.Repeat("中", 5000)
	if err := card.Finalize(ctx, medium); err != nil {
		t.Fatal(err)
	}
	if (*calls)[1].path != "/chat.update" {
		t.Fatal("native update retained legacy byte cap")
	}
	assertNative(t, (*calls)[1], medium)
	huge := strings.Repeat("中", slackMarkdownMaxChars+100)
	if err := card.Finalize(ctx, huge); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 4 {
		t.Fatalf("calls=%d", len(*calls))
	}
	var combined string
	for _, m := range (*calls)[2:] {
		var blocks []markdownBlock
		_ = json.Unmarshal([]byte(m.blocks), &blocks)
		combined += blocks[0].Text
		if utf8.RuneCountInString(blocks[0].Text) > slackMarkdownMaxChars || m.thread != "1.0" {
			t.Fatal("invalid overflow post")
		}
	}
	if combined != huge {
		t.Fatal("long final reply truncated")
	}
}

func TestNativeMarkdown_RejectedLongUpdatePostsLegacyFinal(t *testing.T) {
	client, calls := markdownTestClient(t, func(n int, m capturedMessage) string {
		if n == 2 {
			return `{"ok":false,"error":"invalid_blocks"}`
		}
		return `{"ok":true,"channel":"C1","ts":"1.2"}`
	})
	renderer, _ := newMessageRenderer("auto")
	card := &slackStreamingCard{client: client, renderer: renderer, channel: "C1", threadTS: "1.0"}
	if err := card.Update(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("中", 5000)
	if err := card.Finalize(context.Background(), content); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 || (*calls)[2].path != "/chat.postMessage" || (*calls)[2].text != content || (*calls)[2].thread != "1.0" {
		t.Fatalf("fallback calls=%#v", *calls)
	}
}
func TestSplitSlackMarkdown_CodeClosingFenceDoesNotSwallowFollowingText(t *testing.T) {
	chunks, err := splitSlackMarkdown("```go\n"+strings.Repeat("x\n", 100)+"```\nAfter", 60)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range chunks {
		if strings.Contains(chunk, "```After") {
			t.Fatal("closing fence joined to following text")
		}
	}
	chunks, err = splitSlackMarkdown("| A |\n| --- |\n"+strings.Repeat("| 中 |\n", 30), 60)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range chunks {
		if strings.Contains(chunk, "| 中 |") && !strings.Contains(chunk, "| --- |") {
			t.Fatal("one-column table lost header")
		}
	}
}

func TestNativeMarkdown_FactoryDefaultsAndConcurrentRejection(t *testing.T) {
	value, err := New(map[string]any{"bot_token": "test", "app_token": "test", "allow_from": "*"})
	if err != nil {
		t.Fatal(err)
	}
	p := value.(*Platform)
	if !p.renderer.native() || p.renderer.mode != "auto" {
		t.Fatal("factory did not enable native Markdown")
	}
	if _, err := New(map[string]any{"bot_token": "test", "app_token": "test", "markdown_mode": "bad"}); err == nil {
		t.Fatal("invalid factory option accepted")
	}
	rejected := slack.SlackErrorResponse{Err: "invalid_blocks"}
	if !p.renderer.fallback(rejected) || !p.renderer.fallback(rejected) {
		t.Fatal("concurrent in-flight block rejections cannot both retry")
	}
}

// Regression: native blocks accept long Unicode answers, but chat.update still
// rejects an oversized top-level notification text. That rejection caused the
// engine to post the entire answer again beside an already visible preview.
func TestNativeMarkdown_LongUnicodeNotificationDoesNotDuplicateFinalReply(t *testing.T) {
	client, calls := markdownTestClient(t, func(_ int, m capturedMessage) string {
		if m.path == "/chat.update" && len(m.text) > slackUpdateMaxText {
			return `{"ok":false,"error":"msg_too_long"}`
		}
		return `{"ok":true,"channel":"C1","ts":"1.2"}`
	})
	renderer, _ := newMessageRenderer("native")
	card := &slackStreamingCard{client: client, renderer: renderer, channel: "C1"}
	content := "## Growth\n\n" + strings.Repeat("中文😀 ~56% **growth**\n", 220)
	if err := card.Update(context.Background(), content); err != nil {
		t.Fatal(err)
	}
	if err := card.Finalize(context.Background(), content+"\nDone"); err != nil {
		t.Fatalf("final update triggered a duplicate fallback: %v", err)
	}
	if len(*calls) != 2 || (*calls)[1].path != "/chat.update" {
		t.Fatalf("reply was posted again: %#v", *calls)
	}
	for _, m := range *calls {
		if len(m.text) > slackUpdateMaxText || !utf8.ValidString(m.text) {
			t.Fatalf("invalid notification text: bytes=%d", len(m.text))
		}
	}
	assertNative(t, (*calls)[1], content+"\nDone")
}
