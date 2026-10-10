package slack

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

// separatedCard uses one editable progress message and fresh final messages.
// Progress delivery is best-effort and cannot suppress or duplicate an answer.
type separatedCard struct {
	*slackStreamingCard
	finalMu     sync.Mutex
	progress    string
	closed      bool
	finalChunks []string
	sent        int
	finalized   bool
}

func (c *separatedCard) UpdateProgress(ctx context.Context, content string) error {
	c.finalMu.Lock()
	defer c.finalMu.Unlock()
	if c.closed || content == "" {
		return nil
	}
	c.progress = content
	return c.slackStreamingCard.Update(ctx, content)
}
func (c *separatedCard) stopProgress(ctx context.Context, completed bool) {
	if c.closed {
		return
	}
	c.closed = true
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ts == "" {
		return
	}
	marker := "❌"
	if completed {
		marker = "✅"
	}
	// Never post a second progress message during cleanup.
	if err := c.renderer.update(ctx, c.client, c.channel, c.ts, marker+"\n\n"+c.progress); err != nil {
		slog.Warn("slack: progress cleanup failed", "error", err)
	}
}
func (c *separatedCard) StopProgress(ctx context.Context, completed bool) {
	c.finalMu.Lock()
	defer c.finalMu.Unlock()
	c.stopProgress(ctx, completed)
}
func (c *separatedCard) FinalizeAnswer(ctx context.Context, progress, answer string) error {
	c.finalMu.Lock()
	defer c.finalMu.Unlock()
	if c.finalized {
		return nil
	}
	c.progress = progress
	c.stopProgress(ctx, true)
	if answer == "" {
		c.finalized = true
		return nil
	}
	if c.finalChunks == nil {
		chunks, err := c.renderer.chunks(answer)
		if err != nil {
			return fmt.Errorf("slack: split final answer: %w", err)
		}
		c.finalChunks = chunks
	}
	for c.sent < len(c.finalChunks) {
		if _, err := c.renderer.post(ctx, c.client, c.channel, c.threadTS, c.finalChunks[c.sent]); err != nil {
			return fmt.Errorf("slack: send final answer: %w", err)
		}
		c.sent++
	}
	c.finalized = true
	return nil
}

var _ core.SeparatedStreamingCard = (*separatedCard)(nil)
