package antigravity

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// agy encodes reasoning effort in the model ID (for example
// gemini-4-argon-high), so effort switching swaps that suffix.
var agyEffortTiers = []string{"low", "medium", "high"}

const (
	agyDefaultEffort   = "medium"
	agyCatalogTTL      = 10 * time.Minute
	agyCatalogTimeout  = 20 * time.Second
	agyCatalogMinRetry = 30 * time.Second
)

var (
	ansiEscape   = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	modelLineRe  = regexp.MustCompile(`^([a-z0-9][a-z0-9._-]*)\t+(.+)$`)
	catalogMu    sync.Mutex
	catalogCache = map[string]*agyCatalogEntry{}
)

type agyCatalogEntry struct {
	models     []core.ModelOption
	fetchedAt  time.Time
	failedAt   time.Time
	refreshing bool
}

func runAgyModels(ctx context.Context, cmd string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, agyCatalogTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, cmd, "models")
	c.Env = append(os.Environ(), env...)
	c.Dir = os.TempDir()
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("agy models: %w", err)
	}
	return string(out), nil
}

// parseAgyModels reads `agy models` output: one "<id>\t<label>" per line,
// possibly mixed with spinner output.
func parseAgyModels(out string) []core.ModelOption {
	out = ansiEscape.ReplaceAllString(out, "")
	out = strings.ReplaceAll(out, "\r", "\n")
	var models []core.ModelOption
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		m := modelLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		models = append(models, core.ModelOption{Name: m[1], Desc: strings.TrimSpace(m[2])})
	}
	return models
}

// agyModelCatalog returns the cached native model list. A stale list is
// returned immediately while one background refresh runs; a failed refresh
// keeps the last good list.
func agyModelCatalog(ctx context.Context, cmd string, env []string) []core.ModelOption {
	catalogMu.Lock()
	entry := catalogCache[cmd]
	if entry == nil {
		entry = &agyCatalogEntry{}
		catalogCache[cmd] = entry
	}
	now := time.Now()
	models := entry.models
	fresh := len(models) > 0 && now.Sub(entry.fetchedAt) < agyCatalogTTL
	recentFailure := !entry.failedAt.IsZero() && now.Sub(entry.failedAt) < agyCatalogMinRetry
	if fresh || recentFailure || entry.refreshing {
		catalogMu.Unlock()
		return models
	}
	entry.refreshing = true
	catalogMu.Unlock()

	if len(models) > 0 {
		go refreshAgyCatalog(context.Background(), cmd, env, entry)
		return models
	}
	return refreshAgyCatalog(ctx, cmd, env, entry)
}

func refreshAgyCatalog(ctx context.Context, cmd string, env []string, entry *agyCatalogEntry) []core.ModelOption {
	out, err := runAgyModels(ctx, cmd, env)
	models := parseAgyModels(out)
	catalogMu.Lock()
	defer catalogMu.Unlock()
	entry.refreshing = false
	if err != nil || len(models) == 0 {
		slog.Warn("antigravity: model catalog unavailable", "error", err, "parsed", len(models))
		entry.failedAt = time.Now()
		return entry.models
	}
	entry.models = models
	entry.fetchedAt = time.Now()
	entry.failedAt = time.Time{}
	return models
}

// prewarmAgyCatalog fills the cache so /model cards do not wait on the CLI.
func prewarmAgyCatalog(cmd string, env []string) {
	go agyModelCatalog(context.Background(), cmd, env)
}

func isEffortTier(s string) bool {
	for _, t := range agyEffortTiers {
		if s == t {
			return true
		}
	}
	return false
}

// splitEffort splits "gemini-4-argon-high" into ("gemini-4-argon", "high").
func splitEffort(id string) (family, effort string) {
	if i := strings.LastIndex(id, "-"); i > 0 && isEffortTier(id[i+1:]) {
		return id[:i], id[i+1:]
	}
	return id, ""
}

// familyAlias derives the short name users type, e.g. gemini-4-argon -> argon,
// claude-opus-5-5 -> opus, gpt-oss-120b -> oss-120b.
func familyAlias(family string) string {
	parts := strings.Split(family, "-")
	var kept []string
	for i, p := range parts {
		if i == 0 && (p == "gemini" || p == "claude" || p == "gpt") {
			continue
		}
		if strings.Trim(p, "0123456789.") == "" {
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, "-")
}

func hasModel(models []core.ModelOption, id string) bool {
	for _, m := range models {
		if m.Name == id {
			return true
		}
	}
	return false
}

// pickEffort chooses the requested tier, else the current tier, else medium,
// else the first tier the family offers.
func pickEffort(models []core.ModelOption, family, requested, current string) (string, error) {
	if requested != "" {
		if hasModel(models, family+"-"+requested) {
			return family + "-" + requested, nil
		}
		return "", fmt.Errorf("%s has no %s effort; available: %s", family, requested, strings.Join(familyEfforts(models, family), ", "))
	}
	for _, e := range []string{current, agyDefaultEffort} {
		if e != "" && hasModel(models, family+"-"+e) {
			return family + "-" + e, nil
		}
	}
	for _, m := range models {
		if f, _ := splitEffort(m.Name); f == family {
			return m.Name, nil
		}
	}
	return family, nil
}

func familyEfforts(models []core.ModelOption, family string) []string {
	var out []string
	for _, t := range agyEffortTiers {
		if hasModel(models, family+"-"+t) {
			out = append(out, t)
		}
	}
	return out
}

func familyAliases(models []core.ModelOption) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range models {
		f, _ := splitEffort(m.Name)
		a := familyAlias(f)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// resolveAgyModel maps user input ("argon", "argon high", "gemini-4-argon",
// a full ID or its label) to a catalog model ID. currentEffort is kept when
// the input names a family without an effort.
func resolveAgyModel(models []core.ModelOption, input, currentEffort string) (string, error) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(input)))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty model name")
	}
	if len(models) == 0 {
		return "", fmt.Errorf("agy model catalog is unavailable; run `agy models` on the host and retry")
	}
	requested := ""
	if len(fields) > 1 && isEffortTier(fields[len(fields)-1]) {
		requested = fields[len(fields)-1]
		fields = fields[:len(fields)-1]
	}
	name := strings.Join(fields, " ")

	for _, m := range models {
		if strings.EqualFold(m.Name, name) || strings.EqualFold(m.Desc, name) {
			if requested == "" {
				return m.Name, nil
			}
			family, _ := splitEffort(m.Name)
			return pickEffort(models, family, requested, "")
		}
	}

	var families []string
	seen := map[string]bool{}
	for _, m := range models {
		family, _ := splitEffort(m.Name)
		if seen[family] {
			continue
		}
		if family == name || familyAlias(family) == name {
			seen[family] = true
			families = append(families, family)
		}
	}
	switch len(families) {
	case 1:
		return pickEffort(models, families[0], requested, currentEffort)
	case 0:
		return "", fmt.Errorf("unknown agy model %q; use a full ID from /model or one of: %s (optionally followed by low|medium|high)", input, strings.Join(familyAliases(models), ", "))
	default:
		return "", fmt.Errorf("ambiguous agy model %q matches %s; use a full ID", input, strings.Join(families, ", "))
	}
}
