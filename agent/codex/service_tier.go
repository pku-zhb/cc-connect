package codex

import (
	"context"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func normalizeServiceTier(tier string) string {
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier == "fast" {
		return "priority"
	}
	return tier
}

func selectedServiceTier(source func() string) string {
	if source == nil {
		return ""
	}
	return normalizeServiceTier(source())
}

func (a *Agent) SetServiceTier(tier string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.serviceTier = normalizeServiceTier(tier)
}

func (a *Agent) configuredServiceTier() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.serviceTier
}

func (a *Agent) GetServiceTier(ctx context.Context) (string, error) {
	a.mu.RLock()
	tier, bin, dir, home := a.serviceTier, a.cmd, a.workDir, a.codexHome
	args := append([]string(nil), a.cliExtraArgs...)
	env := append([]string(nil), a.configEnv...)
	env = append(env, a.providerEnvLocked()...)
	env = append(env, a.sessionEnv...)
	a.mu.RUnlock()
	if tier != "" {
		return tier, nil
	}
	if home != "" {
		env = core.MergeEnv(env, []string{"CODEX_HOME=" + home})
	}
	settings, err := loadCodexRuntimeSettings(ctx, bin, args, dir, env)
	if err != nil {
		return "", err
	}
	tier = normalizeServiceTier(settings.ServiceTier)
	if tier == "" {
		tier = "default"
	}
	return tier, nil
}

func (s *appServerSession) setReportedServiceTier(tier *string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.serviceTier = normalizeServiceTier(stringValue(tier))
	if s.serviceTier == "" {
		s.serviceTier = "default"
	}
}

func (s *appServerSession) GetServiceTier(_ context.Context) (string, error) {
	if tier := selectedServiceTier(s.tierSource); tier != "" {
		return tier, nil
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.serviceTier, nil
}

var _ core.ServiceTierSwitcher = (*Agent)(nil)
var _ core.ServiceTierReporter = (*appServerSession)(nil)

func (s *codexSession) GetServiceTier(ctx context.Context) (string, error) {
	if tier := selectedServiceTier(s.tierSource); tier != "" {
		return tier, nil
	}
	settings, err := loadCodexRuntimeSettings(ctx, s.cmd, s.cliExtraArgs, s.workDir, s.extraEnv)
	if err != nil {
		return "", err
	}
	tier := normalizeServiceTier(settings.ServiceTier)
	if tier == "" {
		tier = "default"
	}
	return tier, nil
}

var _ core.ServiceTierReporter = (*codexSession)(nil)
