package slack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestSeparatedCard_FinalIsFreshAndNeverAppearsInProgress(t *testing.T) {
	client, calls := markdownTestClient(t, nil)
	renderer, _ := newMessageRenderer("native")
	p := &Platform{client: client, renderer: renderer, separateFinal: true}
	raw, err := p.CreateStreamingCard(context.Background(), replyContext{channel: "C1", timestamp: "1.0"})
	if err != nil {
		t.Fatal(err)
	}
	card := raw.(core.SeparatedStreamingCard)
	if err := card.UpdateProgress(context.Background(), "Checking sources"); err != nil {
		t.Fatal(err)
	}
	if err := card.FinalizeAnswer(context.Background(), "Checking sources", "## Final\nAnswer"); err != nil {
		t.Fatal(err)
	}
	if err := card.FinalizeAnswer(context.Background(), "Checking sources", "## Final\nAnswer"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 {
		t.Fatalf("calls=%#v", *calls)
	}
	if (*calls)[0].path != "/chat.postMessage" || (*calls)[1].path != "/chat.update" || (*calls)[2].path != "/chat.postMessage" {
		t.Fatal("final reused progress message")
	}
	if strings.Contains((*calls)[1].blocks, "Answer") {
		t.Fatal("final duplicated in progress")
	}
	assertNative(t, (*calls)[2], "## Final\nAnswer")
	if (*calls)[2].thread != "1.0" {
		t.Fatal("final lost thread")
	}
}
func TestSeparatedCard_ProgressErrorCannotSuppressFinal(t *testing.T) {
	client, calls := markdownTestClient(t, func(n int, m capturedMessage) string {
		if n == 1 {
			return `{"ok":false,"error":"ratelimited"}`
		}
		return `{"ok":true,"channel":"C1","ts":"1.2"}`
	})
	renderer, _ := newMessageRenderer("native")
	card := &separatedCard{slackStreamingCard: &slackStreamingCard{client: client, renderer: renderer, channel: "C1"}}
	_ = card.UpdateProgress(context.Background(), "progress")
	if err := card.FinalizeAnswer(context.Background(), "progress", "answer"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatal("final lost or duplicated")
	}
	assertNative(t, (*calls)[1], "answer")
}
func TestSeparatedCard_PartialFinalRetryDoesNotRepeatSentChunks(t *testing.T) {
	client, calls := markdownTestClient(t, func(n int, m capturedMessage) string {
		if n == 2 {
			return `{"ok":false,"error":"ratelimited"}`
		}
		return `{"ok":true,"channel":"C1","ts":"1.2"}`
	})
	renderer, _ := newMessageRenderer("native")
	card := &separatedCard{slackStreamingCard: &slackStreamingCard{client: client, renderer: renderer, channel: "C1"}}
	answer := strings.Repeat("a", slackMarkdownMaxChars) + "tail"
	if err := card.FinalizeAnswer(context.Background(), "", answer); err == nil {
		t.Fatal("expected send failure")
	}
	if err := card.FinalizeAnswer(context.Background(), "", answer); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 {
		t.Fatalf("calls=%d", len(*calls))
	}
	var blocks []markdownBlock
	_ = json.Unmarshal([]byte((*calls)[2].blocks), &blocks)
	if blocks[0].Text != "tail" {
		t.Fatal("already sent first chunk repeated")
	}
}
func TestSeparatedCard_BothHiddenOrSilentDoesNotCreateProgress(t *testing.T) {
	client, calls := markdownTestClient(t, nil)
	renderer, _ := newMessageRenderer("native")
	for _, answer := range []string{"answer", ""} {
		card := &separatedCard{slackStreamingCard: &slackStreamingCard{client: client, renderer: renderer, channel: "C1"}}
		if err := card.FinalizeAnswer(context.Background(), "", answer); err != nil {
			t.Fatal(err)
		}
	}
	if len(*calls) != 1 {
		t.Fatal("empty progress or silent final was posted")
	}
}
