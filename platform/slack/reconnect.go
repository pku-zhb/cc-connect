package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

const maxReconnectBackoff = 5 * time.Minute

// runSocketMode bounds the SDK's network retry delay. slack-go v0.16.0
// calculates exponential backoff without applying its configured maximum.
// Cancel only a failed connection attempt, then wait here before starting a
// fresh retry loop; a healthy connection and server Retry-After are preserved.
func runSocketMode(ctx context.Context, run func(context.Context) error, events <-chan socketmode.Event, handle func(socketmode.Event)) error {
	capped := false
retry:
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- run(runCtx) }()

		for {
			select {
			case <-ctx.Done():
				cancel()
				<-done
				return ctx.Err()
			case err := <-done:
				cancel()
				return err
			case evt, ok := <-events:
				if !ok {
					cancel()
					<-done
					return fmt.Errorf("slack: socket mode event channel closed")
				}
				handle(evt)
				if evt.Type == socketmode.EventTypeConnected {
					capped = false
				}
				failure, ok := evt.Data.(*slackapi.ConnectionErrorEvent)
				if evt.Type != socketmode.EventTypeConnectionError || !ok {
					continue
				}
				var rateLimit *slackapi.RateLimitedError
				if errors.As(failure.ErrorObj, &rateLimit) {
					continue // Slack may require a wait longer than our network retry cap.
				}
				if !capped && failure.Backoff > 0 && failure.Backoff <= maxReconnectBackoff {
					continue
				}

				cancel()
				<-done // Never overlap SDK runners or leave one sleeping for hours.
				capped = true
				slog.Warn("slack: reconnect delay capped", "attempt", failure.Attempt,
					"sdk_backoff", failure.Backoff, "retry_after", maxReconnectBackoff)
				timer := time.NewTimer(maxReconnectBackoff)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
					continue retry
				}
			}
		}
	}
}
