package main

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/platform"
)

func TestDoctorEnvCommandCheckSuccess(t *testing.T) {
	origExec := execInEnvironmentTarget
	defer func() { execInEnvironmentTarget = origExec }()

	execInEnvironmentTarget = func(envID, workDir string, timeout int, command []string) (*platform.ExecResult, error) {
		return &platform.ExecResult{Stdout: "/usr/local/bin/claude\n"}, nil
	}

	check := doctorEnvCommandCheck("env-123", "claude", []string{"which", "claude"}, "claude missing in image")
	if check.Status != doctorOK {
		t.Fatalf("status = %q", check.Status)
	}
	if check.Detail != "/usr/local/bin/claude" {
		t.Fatalf("detail = %q", check.Detail)
	}
}

func TestDoctorEnvCommandCheckMissingBinary(t *testing.T) {
	origExec := execInEnvironmentTarget
	defer func() { execInEnvironmentTarget = origExec }()

	execInEnvironmentTarget = func(envID, workDir string, timeout int, command []string) (*platform.ExecResult, error) {
		return &platform.ExecResult{ExitCode: 1}, nil
	}

	check := doctorEnvCommandCheck("env-123", "claude", []string{"which", "claude"}, "claude missing in image")
	if check.Status != doctorWarn {
		t.Fatalf("status = %q", check.Status)
	}
	if check.Detail != "claude missing in image" {
		t.Fatalf("detail = %q", check.Detail)
	}
}

func TestDoctorCompletionCheckUsesInstalledPath(t *testing.T) {
	origShell := doctorShell
	origStat := doctorStat
	defer func() {
		doctorShell = origShell
		doctorStat = origStat
	}()

	doctorShell = func() string { return "zsh" }
	doctorStat = func(name string) (fs.FileInfo, error) {
		if strings.HasSuffix(name, "_cw") {
			return fakeFileInfo{name: "_cw"}, nil
		}
		return nil, fmt.Errorf("missing")
	}

	check := doctorCompletionCheck()
	if check.Status != doctorOK {
		t.Fatalf("status = %q", check.Status)
	}
	if !strings.Contains(check.Detail, "_cw") {
		t.Fatalf("detail = %q", check.Detail)
	}
}

type fakeFileInfo struct {
	name string
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }
