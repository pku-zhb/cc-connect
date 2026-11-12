package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

const sampleAgyModels = "\x1b[K⠋ Fetching available models...\r\x1b[K" +
	"gemini-4-carbon-medium\tGemini 4 Carbon (Medium)\n" +
	"gemini-4-carbon-high\tGemini 4 Carbon (High)\n" +
	"gemini-4-carbon-low\tGemini 4 Carbon (Low)\n" +
	"gemini-4-argon-medium\tGemini 4 Argon (Medium)\n" +
	"gemini-4-argon-high\tGemini 4 Argon (High)\n" +
	"gemini-4-argon-low\tGemini 4 Argon (Low)\n" +
	"gemini-3.8-flash-high\tGemini 3.8 Flash (High)\n" +
	"claude-opus-5-5-low\tClaude Opus 5.5 (Low)\n" +
	"gpt-oss-120b-medium\tGPT-OSS 120B (Medium)\n"

func TestParseAgyModelsIgnoresSpinner(t *testing.T) {
	models := parseAgyModels(sampleAgyModels)
	if len(models) != 9 {
		t.Fatalf("parsed %d models: %v", len(models), models)
	}
	if models[0].Name != "gemini-4-carbon-medium" || models[0].Desc != "Gemini 4 Carbon (Medium)" {
		t.Fatalf("first model = %+v", models[0])
	}
}

func TestResolveAgyModel(t *testing.T) {
	models := parseAgyModels(sampleAgyModels)
	tests := []struct {
		input, current, want string
	}{
		{"gemini-4-argon-high", "", "gemini-4-argon-high"},
		{"Gemini 4 Argon (Low)", "", "gemini-4-argon-low"},
		{"argon", "", "gemini-4-argon-medium"},
		{"argon", "high", "gemini-4-argon-high"},
		{"Argon High", "low", "gemini-4-argon-high"},
		{"gemini-4-carbon", "", "gemini-4-carbon-medium"},
		{"gemini-4-carbon-medium low", "", "gemini-4-carbon-low"},
		{"flash", "", "gemini-3.8-flash-high"},
		{"opus", "medium", "claude-opus-5-5-low"},
		{"oss-120b", "", "gpt-oss-120b-medium"},
	}
	for _, tt := range tests {
		got, err := resolveAgyModel(models, tt.input, tt.current)
		if err != nil || got != tt.want {
			t.Errorf("resolveAgyModel(%q, %q) = %q, %v; want %q", tt.input, tt.current, got, err, tt.want)
		}
	}
	for _, bad := range []string{"argonx", "gemini-3.1-pro-preview", "flash low"} {
		if got, err := resolveAgyModel(models, bad, ""); err == nil {
			t.Errorf("resolveAgyModel(%q) = %q, want error", bad, got)
		}
	}
	_, err := resolveAgyModel(models, "argonx", "")
	if err == nil || !strings.Contains(err.Error(), "carbon, argon") {
		t.Fatalf("unknown-model error should list families, got %v", err)
	}
	if _, err := resolveAgyModel(nil, "argon", ""); err == nil {
		t.Fatal("empty catalog must reject the switch")
	}
}

func writeFakeAgy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agy")
	script := "#!/bin/sh\nif [ \"$1\" = models ]; then printf '%s' \"$FAKE_AGY_MODELS\"; fi\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentModelAndEffortSelection(t *testing.T) {
	cmd := writeFakeAgy(t)
	t.Setenv("FAKE_AGY_MODELS", sampleAgyModels)
	agentIface, err := New(map[string]any{"cmd": cmd, "work_dir": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a := agentIface.(*Agent)
	var _ core.ModelResolver = a
	var _ core.LiveReasoningEffortSwitcher = a

	if got := a.AvailableModels(context.Background()); len(got) != 9 {
		t.Fatalf("AvailableModels = %v", got)
	}
	resolved, err := a.ResolveModel(context.Background(), "argon")
	if err != nil || resolved != "gemini-4-argon-medium" {
		t.Fatalf("ResolveModel(argon) = %q, %v", resolved, err)
	}
	a.SetModel(resolved)
	a.SetReasoningEffort("high")
	if a.GetModel() != "gemini-4-argon-high" || a.GetReasoningEffort() != "high" {
		t.Fatalf("effort switch: model=%q effort=%q", a.GetModel(), a.GetReasoningEffort())
	}
	resolved, err = a.ResolveModel(context.Background(), "carbon")
	if err != nil || resolved != "gemini-4-carbon-high" {
		t.Fatalf("family switch should keep effort, got %q, %v", resolved, err)
	}
	if _, err := a.ResolveModel(context.Background(), "argonx"); err == nil {
		t.Fatal("unknown model accepted")
	}

	s, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	as := s.(*antigravitySession)
	args := as.buildAntigravityArgs("", false, "yolo", "", as.currentModel(), "hi")
	if !containsPair(args, "--model", "gemini-4-argon-high") {
		t.Fatalf("--model missing: %v", args)
	}
	a.SetModel("gemini-4-carbon-low")
	if as.currentModel() != "gemini-4-carbon-low" {
		t.Fatalf("existing session should follow the agent model, got %q", as.currentModel())
	}
}

func TestNewAppliesPersistedEffortToModel(t *testing.T) {
	cmd := writeFakeAgy(t)
	agentIface, err := New(map[string]any{"cmd": cmd, "work_dir": t.TempDir(), "model": "gemini-4-argon-medium", "reasoning_effort": "low"})
	if err != nil {
		t.Fatal(err)
	}
	if got := agentIface.(*Agent).GetModel(); got != "gemini-4-argon-low" {
		t.Fatalf("model = %q", got)
	}
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
