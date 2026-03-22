package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	cwconfig "github.com/codewiresh/codewire/internal/config"
	"github.com/codewiresh/codewire/internal/platform"
	"github.com/spf13/cobra"
)

type doctorStatus string

const (
	doctorOK   doctorStatus = "ok"
	doctorWarn doctorStatus = "warn"
	doctorFail doctorStatus = "fail"
	doctorSkip doctorStatus = "skip"
)

type doctorCheck struct {
	Name   string
	Status doctorStatus
	Detail string
}

var (
	doctorShell = func() string {
		return strings.ToLower(filepath.Base(os.Getenv("SHELL")))
	}
	doctorStat        = os.Stat
	doctorReadFile    = os.ReadFile
	doctorDialTimeout = net.DialTimeout
)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check Codewire CLI setup and current target health",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCLIConfigForTarget()
			if err != nil {
				cfg = &cwconfig.Config{}
			}
			target := currentTargetConfig(cfg)
			env := lookupEnvironmentForTarget(target)

			fmt.Printf("%s %s\n\n", bold("Current target:"), targetSummaryLine(target, env))

			checks := []doctorCheck{
				doctorPlatformCheck(),
				doctorOrgCheck(),
				doctorCompletionCheck(),
				doctorSSHConfigCheck(),
				doctorLocalNodeCheck(),
			}
			if target.Kind == "env" {
				checks = append(checks, doctorEnvironmentChecks(target, env)...)
			}

			for _, check := range checks {
				fmt.Printf("%-12s %-6s %s\n", bold(check.Name), doctorStatusLabel(check.Status), check.Detail)
			}
			return nil
		},
	}
}

func doctorPlatformCheck() doctorCheck {
	client, err := platform.NewClient()
	if err != nil {
		return doctorCheck{Name: "platform", Status: doctorFail, Detail: err.Error()}
	}
	resp, err := client.GetSession()
	if err != nil {
		return doctorCheck{Name: "platform", Status: doctorFail, Detail: fmt.Sprintf("session check failed: %v", err)}
	}
	if resp.User == nil {
		return doctorCheck{Name: "platform", Status: doctorFail, Detail: "not logged in"}
	}
	name := resp.User.Name
	if strings.TrimSpace(name) == "" {
		name = resp.User.Email
	}
	return doctorCheck{Name: "platform", Status: doctorOK, Detail: fmt.Sprintf("%s on %s", name, client.ServerURL)}
}

func doctorOrgCheck() doctorCheck {
	orgID, client, err := getDefaultOrg()
	if err != nil {
		return doctorCheck{Name: "org", Status: doctorWarn, Detail: err.Error()}
	}
	org, err := client.GetOrg(orgID)
	if err != nil {
		return doctorCheck{Name: "org", Status: doctorWarn, Detail: fmt.Sprintf("%s (%v)", orgID, err)}
	}
	name := org.Name
	if strings.TrimSpace(name) == "" {
		name = org.Slug
	}
	if name == "" {
		name = org.ID
	}
	return doctorCheck{Name: "org", Status: doctorOK, Detail: name}
}

func doctorCompletionCheck() doctorCheck {
	shell := doctorShell()
	path := detectInstalledCompletionPath(shell)
	if shell == "" {
		return doctorCheck{Name: "completion", Status: doctorWarn, Detail: "unknown shell"}
	}
	if path == "" {
		return doctorCheck{Name: "completion", Status: doctorWarn, Detail: fmt.Sprintf("%s completion not installed", shell)}
	}
	return doctorCheck{Name: "completion", Status: doctorOK, Detail: fmt.Sprintf("%s at %s", shell, path)}
}

func doctorSSHConfigCheck() doctorCheck {
	configPath := defaultSSHConfigPath()
	content, err := doctorReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return doctorCheck{Name: "ssh config", Status: doctorWarn, Detail: fmt.Sprintf("missing %s", configPath)}
		}
		return doctorCheck{Name: "ssh config", Status: doctorWarn, Detail: err.Error()}
	}
	text := string(content)
	if !strings.Contains(text, sshConfigMarkerStart) || !strings.Contains(text, sshConfigMarkerEnd) {
		return doctorCheck{Name: "ssh config", Status: doctorWarn, Detail: fmt.Sprintf("Codewire section missing in %s", configPath)}
	}
	return doctorCheck{Name: "ssh config", Status: doctorOK, Detail: configPath}
}

func doctorLocalNodeCheck() doctorCheck {
	sockPath := filepath.Join(dataDir(), "codewire.sock")
	if _, err := doctorStat(sockPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return doctorCheck{Name: "local node", Status: doctorWarn, Detail: "not running"}
		}
		return doctorCheck{Name: "local node", Status: doctorWarn, Detail: err.Error()}
	}
	conn, err := doctorDialTimeout("unix", sockPath, 300*time.Millisecond)
	if err != nil {
		return doctorCheck{Name: "local node", Status: doctorWarn, Detail: fmt.Sprintf("socket present but unreachable: %v", err)}
	}
	_ = conn.Close()
	return doctorCheck{Name: "local node", Status: doctorOK, Detail: sockPath}
}

func doctorEnvironmentChecks(target *cwconfig.CurrentTargetConfig, env *platform.Environment) []doctorCheck {
	checks := []doctorCheck{}

	if env == nil {
		checks = append(checks, doctorCheck{Name: "env state", Status: doctorWarn, Detail: targetSummaryLine(target, nil)})
	} else {
		checks = append(checks, doctorCheck{Name: "env state", Status: doctorOK, Detail: fmt.Sprintf("%s %s", env.Type, env.State)})
		if env.State != "running" {
			checks = append(checks,
				doctorCheck{Name: "codewire", Status: doctorSkip, Detail: "environment not running"},
				doctorCheck{Name: "claude", Status: doctorSkip, Detail: "environment not running"},
				doctorCheck{Name: "docker", Status: doctorSkip, Detail: "environment not running"},
			)
			return checks
		}
	}

	checks = append(checks,
		doctorEnvCommandCheck(target.Ref, "codewire", []string{"which", "cw"}, "codewire CLI missing in image"),
		doctorEnvCommandCheck(target.Ref, "claude", []string{"which", "claude"}, "claude missing in image"),
		doctorEnvCommandCheck(target.Ref, "docker", []string{"sh", "-lc", "test -S /var/run/docker.sock && echo /var/run/docker.sock"}, "docker socket missing"),
	)
	return checks
}

func doctorEnvCommandCheck(envID, name string, command []string, missingDetail string) doctorCheck {
	result, err := execInEnvironmentTarget(envID, "/workspace", 10, command)
	if err != nil {
		return doctorCheck{Name: name, Status: doctorWarn, Detail: err.Error()}
	}
	if result.ExitCode != 0 {
		return doctorCheck{Name: name, Status: doctorWarn, Detail: missingDetail}
	}
	detail := strings.TrimSpace(result.Stdout)
	if detail == "" {
		detail = strings.TrimSpace(result.Stderr)
	}
	if detail == "" {
		detail = "available"
	}
	return doctorCheck{Name: name, Status: doctorOK, Detail: detail}
}

func detectInstalledCompletionPath(shell string) string {
	switch shell {
	case "zsh":
		for _, candidate := range zshCompletionInstallCandidates() {
			path := filepath.Join(candidate.dir, "_cw")
			if _, err := doctorStat(path); err == nil {
				return path
			}
		}
	case "bash":
		for _, dir := range []string{
			filepath.Join(brewPrefix(), "etc/bash_completion.d"),
			filepath.Join(os.Getenv("HOME"), ".bash_completion.d"),
		} {
			if strings.TrimSpace(dir) == "" {
				continue
			}
			path := filepath.Join(dir, "cw")
			if _, err := doctorStat(path); err == nil {
				return path
			}
		}
	case "fish":
		path := filepath.Join(os.Getenv("HOME"), ".config", "fish", "completions", "cw.fish")
		if _, err := doctorStat(path); err == nil {
			return path
		}
	}
	return ""
}

func doctorStatusLabel(status doctorStatus) string {
	switch status {
	case doctorOK:
		return green(string(status))
	case doctorWarn:
		return yellow(string(status))
	case doctorFail:
		return red(string(status))
	default:
		return dim(string(status))
	}
}
