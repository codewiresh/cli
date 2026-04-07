package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	cwconfig "github.com/codewiresh/codewire/internal/config"
)

var localGOOS = runtime.GOOS

type limaListEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func limaInstanceName(instance *cwconfig.LocalInstance) string {
	if instance == nil {
		return ""
	}
	if strings.TrimSpace(instance.LimaInstanceName) != "" {
		return strings.TrimSpace(instance.LimaInstanceName)
	}
	if strings.TrimSpace(instance.RuntimeName) != "" {
		return strings.TrimSpace(instance.RuntimeName)
	}
	return strings.TrimSpace(instance.Name)
}

func defaultLimaVMType() string {
	if localGOOS == "darwin" {
		return "vz"
	}
	return "qemu"
}

func defaultLimaMountType(vmType string) string {
	if vmType == "vz" {
		return "virtiofs"
	}
	return "9p"
}

func limaCreateCommandArgs(instance *cwconfig.LocalInstance) []string {
	vmType := strings.TrimSpace(instance.LimaVMType)
	if vmType == "" {
		vmType = defaultLimaVMType()
	}
	mountType := strings.TrimSpace(instance.LimaMountType)
	if mountType == "" {
		mountType = defaultLimaMountType(vmType)
	}

	mountSet := fmt.Sprintf(
		`.mounts=[{"location":%s,"mountPoint":"/workspace","writable":true}]`,
		strconv.Quote(instance.RepoPath),
	)

	args := []string{
		"start",
		"--tty=false",
		"--name", limaInstanceName(instance),
		"--vm-type", vmType,
		"--mount-type", mountType,
		"--mount-none",
		"--set", mountSet,
	}

	if instance.CPU > 0 {
		cpus := (instance.CPU + 999) / 1000
		if cpus < 1 {
			cpus = 1
		}
		args = append(args, "--cpus", strconv.Itoa(cpus))
	}
	if instance.Memory > 0 {
		memGiB := (instance.Memory + 1023) / 1024
		if memGiB < 1 {
			memGiB = 1
		}
		args = append(args, "--memory", strconv.Itoa(memGiB))
	}
	if instance.Disk > 0 {
		args = append(args, "--disk", strconv.Itoa(instance.Disk))
	}
	for _, port := range instance.Ports {
		if port.Port <= 0 {
			continue
		}
		args = append(args, "--port-forward", fmt.Sprintf("%d:%d,static=true", port.Port, port.Port))
	}

	return append(args, "template://default")
}

func createLocalLimaInstance(instance *cwconfig.LocalInstance) error {
	if _, err := localLookPath("limactl"); err != nil {
		return fmt.Errorf("limactl is required for the lima backend: %w", err)
	}

	instance.LimaInstanceName = limaInstanceName(instance)
	instance.LimaVMType = defaultLimaVMType()
	instance.LimaMountType = defaultLimaMountType(instance.LimaVMType)

	args := limaCreateCommandArgs(instance)
	out, err := localRunCommand("limactl", args...)
	if err != nil {
		return fmt.Errorf("limactl %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func startLocalLimaInstance(instance *cwconfig.LocalInstance) error {
	if _, err := localLookPath("limactl"); err != nil {
		return fmt.Errorf("limactl is required for the lima backend: %w", err)
	}
	name := limaInstanceName(instance)
	out, err := localRunCommand("limactl", "start", "--tty=false", name)
	if err != nil {
		return fmt.Errorf("limactl start --tty=false %s: %v\n%s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func stopLocalLimaInstance(instance *cwconfig.LocalInstance) error {
	if _, err := localLookPath("limactl"); err != nil {
		return fmt.Errorf("limactl is required for the lima backend: %w", err)
	}
	name := limaInstanceName(instance)
	out, err := localRunCommand("limactl", "stop", name)
	if err != nil {
		return fmt.Errorf("limactl stop %s: %v\n%s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func deleteLocalLimaInstance(instance *cwconfig.LocalInstance) error {
	if _, err := localLookPath("limactl"); err != nil {
		return fmt.Errorf("limactl is required for the lima backend: %w", err)
	}
	name := limaInstanceName(instance)
	out, err := localRunCommand("limactl", "delete", "--force", name)
	if err != nil {
		lower := strings.ToLower(string(out))
		if strings.Contains(lower, "not found") || strings.Contains(lower, "no such") {
			return nil
		}
		return fmt.Errorf("limactl delete --force %s: %v\n%s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func limaInstanceStatus(instance *cwconfig.LocalInstance) (string, error) {
	if _, err := localLookPath("limactl"); err != nil {
		return "", fmt.Errorf("limactl is required for the lima backend: %w", err)
	}
	name := limaInstanceName(instance)
	out, err := localRunCommand("limactl", "list", "--format", "json", name)
	if err != nil {
		lower := strings.ToLower(string(out))
		if strings.Contains(lower, "not found") || strings.Contains(lower, "no such") {
			return "missing", nil
		}
		return "", fmt.Errorf("limactl list --format json %s: %v\n%s", name, err, strings.TrimSpace(string(out)))
	}

	data := bytes.TrimSpace(out)
	if len(data) == 0 {
		return "missing", nil
	}

	var entries []limaListEntry
	if data[0] == '{' {
		var entry limaListEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return "", fmt.Errorf("parse limactl list output: %w", err)
		}
		entries = []limaListEntry{entry}
	} else {
		if err := json.Unmarshal(data, &entries); err != nil {
			return "", fmt.Errorf("parse limactl list output: %w", err)
		}
	}

	for _, entry := range entries {
		if strings.TrimSpace(entry.Name) == name {
			status := strings.ToLower(strings.TrimSpace(entry.Status))
			if status == "" {
				return "unknown", nil
			}
			return status, nil
		}
	}

	return "missing", nil
}
