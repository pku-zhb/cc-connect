package slack

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSeparatedCard_ThoughtUpdatesEditOneProgressAndPostIndependentFinal(t *testing.T) {
	client, calls := markdownTestClient(t, nil)
	renderer, _ := newMessageRenderer("native")
	p := &Platform{client: client, renderer: renderer, separateFinal: true}
	raw, err := p.CreateStreamingCard(context.Background(), replyContext{channel: "C1", timestamp: "thread-1"})
	if err != nil {
		t.Fatal(err)
	}
	card := raw.(*separatedCard)
	progress := ""
	for i := 0; i < 12; i++ {
		progress += fmt.Sprintf("💭 Thought stage %d.\n\n", i)
		// Exercise real update requests without waiting for the UI throttle.
		card.lastUpdate = time.Time{}
		if err := card.UpdateProgress(context.Background(), progress); err != nil {
			t.Fatal(err)
		}
	}
	final := "## 最终答案\n\n独立的中文结论。"
	if err := card.FinalizeAnswer(context.Background(), progress, final); err != nil {
		t.Fatal(err)
	}
	// Neither a late thought nor a repeated completion may create another reply.
	_ = card.UpdateProgress(context.Background(), "late thought")
	_ = card.FinalizeAnswer(context.Background(), progress, final)
	posts, updates := 0, 0
	for _, call := range *calls {
		switch call.path {
		case "/chat.postMessage":
			posts++
			if posts == 1 {
				assertNative(t, call, "💭 Thought stage 0.\n\n")
			} else {
				assertNative(t, call, final)
			}
			if call.thread != "thread-1" {
				t.Fatalf("reply escaped thread: %+v", call)
			}
		case "/chat.update":
			updates++
			if strings.Contains(call.blocks, "最终答案") || strings.Contains(call.blocks, "独立的中文结论") {
				t.Fatal("final answer leaked into progress")
			}
		default:
			t.Fatalf("unexpected API: %s", call.path)
		}
	}
	if posts != 2 || updates != 12 {
		t.Fatalf("posts=%d updates=%d: expected one progress post, in-place updates, and one final post", posts, updates)
	}
	assertNative(t, (*calls)[len(*calls)-2], progress)
}
