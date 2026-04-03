package main

import (
	"os"
	"strings"
	"testing"

	cwconfig "github.com/codewiresh/codewire/internal/config"
	"github.com/codewiresh/codewire/internal/platform"
)

func TestBuildEnvironmentRunCommandIncludesFlags(t *testing.T) {
	got := buildEnvironmentRunCommand(
		"/workspace/app",
		"planner",
		"mesh",
		[]string{"FOO=bar", "A=B"},
		[]string{"alpha", "beta"},
		[]string{"claude", "--version"},
	)

	want := []string{
		"cw", "run",
		"--dir", "/workspace/app",
		"--name", "planner",
		"--group", "mesh",
		"--env", "FOO=bar",
		"--env", "A=B",
		"--tag", "alpha",
		"--tag", "beta",
		"--",
		"claude", "--version",
	}

	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestRunCmdUsesCurrentEnvironmentTarget(t *testing.T) {
	origLoad := loadCLIConfigForTarget
	origRunEnv := runInEnvironmentTarget
	origServerFlag := serverFlag
	defer func() {
		loadCLIConfigForTarget = origLoad
		runInEnvironmentTarget = origRunEnv
		serverFlag = origServerFlag
	}()

	serverFlag = ""
	loadCLIConfigForTarget = func() (*cwconfig.Config, error) {
		return &cwconfig.Config{
			CurrentTarget: &cwconfig.CurrentTargetConfig{
				Kind: "env",
				Ref:  "f062947a-60e2-405c-b89d-5f48b493d8fb",
				Name: "env-fb08",
			},
		}, nil
	}

	var gotEnvID, gotWorkDir, gotName, gotGroup string
	var gotEnvVars, gotTags, gotCommand []string
	runInEnvironmentTarget = func(envID, workDir, name, group string, envVars []string, tags []string, command []string) (*platform.ExecResult, error) {
		gotEnvID = envID
		gotWorkDir = workDir
		gotName = name
		gotGroup = group
		gotEnvVars = append([]string(nil), envVars...)
		gotTags = append([]string(nil), tags...)
		gotCommand = append([]string(nil), command...)
		return &platform.ExecResult{}, nil
	}

	cmd := runCmd()
	cmd.SetArgs([]string{"--name", "planner", "--group", "mesh", "--tag", "alpha", "--env", "FOO=bar", "--", "claude", "--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run command failed: %v", err)
	}

	if gotEnvID != "f062947a-60e2-405c-b89d-5f48b493d8fb" {
		t.Fatalf("envID = %q", gotEnvID)
	}
	if gotWorkDir != "/workspace" {
		t.Fatalf("workdir = %q, want /workspace", gotWorkDir)
	}
	if gotName != "planner" {
		t.Fatalf("name = %q, want planner", gotName)
	}
	if gotGroup != "mesh" {
		t.Fatalf("group = %q, want mesh", gotGroup)
	}
	if len(gotEnvVars) != 1 || gotEnvVars[0] != "FOO=bar" {
		t.Fatalf("env vars = %#v", gotEnvVars)
	}
	if len(gotTags) != 2 || gotTags[0] != "alpha" || gotTags[1] != "group:mesh" {
		t.Fatalf("tags = %#v", gotTags)
	}
	if strings.Join(gotCommand, "\x00") != strings.Join([]string{"claude", "--version"}, "\x00") {
		t.Fatalf("command = %#v", gotCommand)
	}
}

func TestRunCmdRequiresNameForGroup(t *testing.T) {
	cmd := runCmd()
	cmd.SetArgs([]string{"--group", "mesh", "--", "claude"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected grouped run without name to fail")
	}
	if !strings.Contains(err.Error(), "--group requires --name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAppendGroupTagDeduplicates(t *testing.T) {
	got := appendGroupTag([]string{"alpha", "group:mesh"}, "mesh")
	if strings.Join(got, "\x00") != strings.Join([]string{"alpha", "group:mesh"}, "\x00") {
		t.Fatalf("tags = %#v", got)
	}
}

func TestRunCmdRejectsPromptFileForEnvironmentTarget(t *testing.T) {
	origLoad := loadCLIConfigForTarget
	origServerFlag := serverFlag
	defer func() {
		loadCLIConfigForTarget = origLoad
		serverFlag = origServerFlag
	}()

	serverFlag = ""
	loadCLIConfigForTarget = func() (*cwconfig.Config, error) {
		return &cwconfig.Config{
			CurrentTarget: &cwconfig.CurrentTargetConfig{
				Kind: "env",
				Ref:  "f062947a-60e2-405c-b89d-5f48b493d8fb",
				Name: "env-fb08",
			},
		}, nil
	}

	promptFile := t.TempDir() + "/prompt.txt"
	if err := os.WriteFile(promptFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	cmd := runCmd()
	cmd.SetArgs([]string{"--prompt-file", promptFile, "--", "claude", "--version"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected prompt-file to be rejected for env targets")
	}
	if !strings.Contains(err.Error(), "--prompt-file is not supported for environment targets yet") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrintEnvironmentRunResultExplainsMissingCodewireCLI(t *testing.T) {
	err := printEnvironmentRunResult(&platform.ExecResult{
		ExitCode: 127,
		Stderr:   "sh: 1: exec: cw: not found",
	})
	if err == nil {
		t.Fatal("expected missing cw error")
	}
	if !strings.Contains(err.Error(), "environment image does not include the codewire CLI") {
		t.Fatalf("unexpected error: %v", err)
	}
}
