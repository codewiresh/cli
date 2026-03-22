package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	cwconfig "github.com/codewiresh/codewire/internal/config"
	"github.com/codewiresh/codewire/internal/platform"
)

func TestCurrentTargetConfigDefaultsToLocal(t *testing.T) {
	target := currentTargetConfig(nil)
	if target.Kind != "local" || target.Ref != "local" {
		t.Fatalf("unexpected default target %#v", target)
	}
}

func TestUseCmdSavesResolvedTarget(t *testing.T) {
	origLoad := loadCLIConfigForTarget
	origSave := saveCLIConfigForTarget
	origResolve := resolveNamedExecutionTarget
	defer func() {
		loadCLIConfigForTarget = origLoad
		saveCLIConfigForTarget = origSave
		resolveNamedExecutionTarget = origResolve
	}()

	loadCLIConfigForTarget = func() (*cwconfig.Config, error) {
		return &cwconfig.Config{}, nil
	}

	var saved *cwconfig.Config
	saveCLIConfigForTarget = func(cfg *cwconfig.Config) error {
		saved = cfg
		return nil
	}

	resolveNamedExecutionTarget = func(ref string) (*cwconfig.CurrentTargetConfig, error) {
		return &cwconfig.CurrentTargetConfig{
			Kind: "env",
			Ref:  "f062947a-60e2-405c-b89d-5f48b493d8fb",
			Name: "env-fb08",
		}, nil
	}

	cmd := useCmd()
	if err := cmd.RunE(cmd, []string{"env-fb08"}); err != nil {
		t.Fatalf("use command failed: %v", err)
	}
	if saved == nil || saved.CurrentTarget == nil {
		t.Fatal("expected current target to be saved")
	}
	if saved.CurrentTarget.Kind != "env" || saved.CurrentTarget.Name != "env-fb08" {
		t.Fatalf("unexpected saved target %#v", saved.CurrentTarget)
	}
}

func TestCurrentCmdPrintsLocalTarget(t *testing.T) {
	origLoad := loadCLIConfigForTarget
	defer func() { loadCLIConfigForTarget = origLoad }()

	loadCLIConfigForTarget = func() (*cwconfig.Config, error) {
		return &cwconfig.Config{}, nil
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	cmd := currentCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("current command failed: %v", err)
	}

	_ = w.Close()
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	got := string(output)
	if !strings.Contains(got, "Kind:") || !strings.Contains(got, "local") {
		t.Fatalf("unexpected output %q", got)
	}
}

func TestTargetCompletionIncludesLocalAndEnvironments(t *testing.T) {
	alpha := "alpha"
	orig := listEnvironmentsForCompletion
	defer func() { listEnvironmentsForCompletion = orig }()

	listEnvironmentsForCompletion = func(cmd *cobra.Command) ([]platform.Environment, error) {
		return []platform.Environment{
			{ID: "f062947a-60e2-405c-b89d-5f48b493d8fb", Name: &alpha},
		}, nil
	}

	got, directive := targetCompletionFunc(useCmd(), nil, "l")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("directive = %v, want no-file completion", directive)
	}
	if len(got) == 0 || got[0] != "local" {
		t.Fatalf("expected local completion first, got %#v", got)
	}
}
