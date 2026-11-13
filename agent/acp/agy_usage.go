package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const agyUsageCacheTTL = 30 * time.Second

// usageCommandRunner runs a one-shot CLI invocation. Tests replace it.
type usageCommandRunner func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error)

func runUsageCommand(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = core.MergeEnv(os.Environ(), env)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr := strings.TrimSpace(string(ee.Stderr))
			if stderr != "" {
				return nil, fmt.Errorf("%w: %s", err, truncateCLIError(stderr))
			}
		}
		return nil, err
	}
	return out, nil
}

func truncateCLIError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 512 {
		return s[:512] + "…"
	}
	return s
}

func (s *acpSession) runUsageCLI(ctx context.Context, args []string) ([]byte, error) {
	runner := s.cliRunner
	if runner == nil {
		runner = runUsageCommand
	}
	return runner(ctx, s.command, args, s.workDir, s.extraEnv)
}

func (s *acpSession) usageProviderName() string {
	name := strings.TrimSpace(s.displayName)
	if name == "" || strings.EqualFold(name, "ACP") {
		return "Antigravity"
	}
	return name
}

func (s *acpSession) agyAccountUsage(ctx context.Context) (*core.UsageReport, error) {
	s.controlsMu.RLock()
	cached, cachedAt := s.agyUsage, s.agyUsageAt
	s.controlsMu.RUnlock()
	if cached != nil && !cachedAt.IsZero() && time.Since(cachedAt) < agyUsageCacheTTL {
		return usageReportWithCountdowns(cached, time.Now()), nil
	}

	report, err := s.fetchAgyUsage(ctx)
	if err != nil {
		return nil, err
	}
	s.controlsMu.Lock()
	s.agyUsage = report
	s.agyUsageAt = time.Now()
	s.controlsMu.Unlock()
	return usageReportWithCountdowns(report, time.Now()), nil
}

func (s *acpSession) fetchAgyUsage(ctx context.Context) (*core.UsageReport, error) {
	if strings.TrimSpace(s.command) == "" {
		return nil, fmt.Errorf("acp: agy usage: agent command is not configured")
	}

	type cliResult struct {
		out []byte
		err error
	}
	usageCh := make(chan cliResult, 1)
	creditsCh := make(chan cliResult, 1)
	go func() {
		out, err := s.runUsageCLI(ctx, []string{"-p", "/usage", "--output-format", "json"})
		usageCh <- cliResult{out, err}
	}()
	go func() {
		out, err := s.runUsageCLI(ctx, []string{"-p", "/credits", "--output-format", "json"})
		creditsCh <- cliResult{out, err}
	}()

	usage := <-usageCh
	if usage.err != nil {
		return nil, fmt.Errorf("acp: agy /usage: %w", usage.err)
	}
	report, err := parseAgyUsageJSON(usage.out, time.Now(), s.usageProviderName())
	if err != nil {
		return nil, err
	}

	// Credits are an additive query on the same backend, not a fallback
	// path. A credits failure leaves usage intact.
	credits := <-creditsCh
	if credits.err != nil {
		return report, nil
	}
	parsed, err := parseAgyCreditsJSON(credits.out)
	if err != nil {
		return report, nil
	}
	report.Credits = parsed
	return report, nil
}

type agyUsageEnvelope struct {
	Status  string `json:"status"`
	Command struct {
		Name string `json:"name"`
		Data struct {
			Groups []agyUsageGroup `json:"groups"`
		} `json:"data"`
	} `json:"command"`
}

type agyUsageGroup struct {
	Name    string        `json:"name"`
	Buckets []agyUsageBin `json:"buckets"`
}

type agyUsageBin struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remaining_fraction"`
	ResetTime         string   `json:"reset_time"`
}

type agyCreditsEnvelope struct {
	Status  string `json:"status"`
	Command struct {
		Name string `json:"name"`
		Data struct {
			RemainingCredits *float64 `json:"remaining_credits"`
		} `json:"data"`
	} `json:"command"`
}

func parseAgyUsageJSON(raw []byte, observedAt time.Time, provider string) (*core.UsageReport, error) {
	var env agyUsageEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("acp: agy /usage: decode: %w", err)
	}
	if err := requireAgyCommandStatus(env.Status, env.Command.Name, "usage"); err != nil {
		return nil, err
	}
	if len(env.Command.Data.Groups) == 0 {
		return nil, fmt.Errorf("acp: agy /usage: no quota groups in response")
	}

	var buckets []core.UsageBucket
	for _, group := range env.Command.Data.Groups {
		bucket, ok, err := usageBucketFromAgyGroup(group)
		if err != nil {
			return nil, err
		}
		if ok {
			buckets = append(buckets, bucket)
		}
	}
	if len(buckets) == 0 {
		return nil, fmt.Errorf("acp: agy /usage: no usable quota windows in response")
	}

	observed := observedAt.Unix()
	if observed < 0 {
		observed = 0
	}
	if provider == "" {
		provider = "Antigravity"
	}
	return &core.UsageReport{
		ObservedAtUnix: observed,
		Provider:       provider,
		Buckets:        buckets,
	}, nil
}

func usageBucketFromAgyGroup(group agyUsageGroup) (core.UsageBucket, bool, error) {
	var windows []core.UsageWindow
	for _, bin := range group.Buckets {
		window, err := usageWindowFromAgyBin(bin)
		if err != nil {
			return core.UsageBucket{}, false, err
		}
		windows = append(windows, window)
	}
	if len(windows) == 0 {
		return core.UsageBucket{}, false, nil
	}
	limitReached := false
	for _, window := range windows {
		if window.UsedPercent >= 100 {
			limitReached = true
			break
		}
	}
	name := strings.TrimSpace(group.Name)
	if name == "" {
		name = "Quota"
	}
	return core.UsageBucket{
		Name:         name,
		Allowed:      !limitReached,
		LimitReached: limitReached,
		Windows:      windows,
	}, true, nil
}

func usageWindowFromAgyBin(bin agyUsageBin) (core.UsageWindow, error) {
	if bin.RemainingFraction == nil {
		return core.UsageWindow{}, fmt.Errorf("acp: agy /usage: bucket %q missing remaining_fraction", firstNonEmpty(bin.ID, bin.Name, bin.Window))
	}
	seconds := agyWindowSeconds(bin.Window)
	name := strings.TrimSpace(bin.Name)
	if name == "" {
		name = strings.TrimSpace(bin.Window)
	}
	window := core.UsageWindow{
		Name:          name,
		UsedPercent:   usedPercentFromRemaining(*bin.RemainingFraction),
		WindowSeconds: seconds,
	}
	if ts := strings.TrimSpace(bin.ResetTime); ts != "" {
		resetAt, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			resetAt, err = time.Parse(time.RFC3339Nano, ts)
		}
		if err != nil {
			return core.UsageWindow{}, fmt.Errorf("acp: agy /usage: bucket %q reset_time: %w", firstNonEmpty(bin.ID, bin.Name), err)
		}
		window.ResetAtUnix = resetAt.Unix()
	}
	return window, nil
}

func parseAgyCreditsJSON(raw []byte) (*core.UsageCredits, error) {
	var env agyCreditsEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("acp: agy /credits: decode: %w", err)
	}
	if err := requireAgyCommandStatus(env.Status, env.Command.Name, "credits"); err != nil {
		return nil, err
	}
	if env.Command.Data.RemainingCredits == nil {
		return nil, fmt.Errorf("acp: agy /credits: missing remaining_credits")
	}
	remaining := *env.Command.Data.RemainingCredits
	return &core.UsageCredits{
		HasCredits: remaining > 0,
		Balance:    formatAgyCreditBalance(remaining),
	}, nil
}

func requireAgyCommandStatus(status, name, want string) error {
	if status != "" && !strings.EqualFold(status, "SUCCESS") {
		return fmt.Errorf("acp: agy /%s: status %q", want, status)
	}
	if name != "" && !strings.EqualFold(name, want) {
		return fmt.Errorf("acp: agy /%s: unexpected command %q", want, name)
	}
	return nil
}

func agyWindowSeconds(window string) int {
	switch strings.ToLower(strings.TrimSpace(window)) {
	case "5h", "five_hour", "five-hour", "five hour":
		return 18000
	case "weekly", "7d", "week":
		return 7 * 24 * 60 * 60
	default:
		return 0
	}
}

func usedPercentFromRemaining(remainingFraction float64) int {
	remaining := int(math.Round(remainingFraction * 100))
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 100 {
		remaining = 100
	}
	return 100 - remaining
}

func formatAgyCreditBalance(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "0"
	}
	if v == math.Trunc(v) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func usageReportWithCountdowns(report *core.UsageReport, now time.Time) *core.UsageReport {
	if report == nil {
		return nil
	}
	result := *report
	result.Buckets = append([]core.UsageBucket(nil), report.Buckets...)
	for i := range result.Buckets {
		result.Buckets[i].Windows = append([]core.UsageWindow(nil), result.Buckets[i].Windows...)
		for j := range result.Buckets[i].Windows {
			window := &result.Buckets[i].Windows[j]
			if window.ResetAtUnix > 0 {
				remaining := window.ResetAtUnix - now.Unix()
				if remaining < 0 {
					remaining = 0
				}
				window.ResetAfterSeconds = int(remaining)
			}
		}
	}
	if report.Credits != nil {
		credits := *report.Credits
		result.Credits = &credits
	}
	return &result
}
