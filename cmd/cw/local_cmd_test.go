package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	cwconfig "github.com/codewiresh/codewire/internal/config"
)

func TestSanitizeLocalName(t *testing.T) {
	if got := sanitizeLocalName("My Repo_name"); got != "my-repo-name" {
		t.Fatalf("sanitizeLocalName() = %q, want %q", got, "my-repo-name")
	}
}

func TestIncusOCIImageRef(t *testing.T) {
	remoteName, remoteURL, remoteImage, err := incusOCIImageRef("ghcr.io/codewiresh/full:latest")
	if err != nil {
		t.Fatalf("incusOCIImageRef() error = %v", err)
	}
	if remoteName != "cw-ghcr-io" {
		t.Fatalf("remoteName = %q, want %q", remoteName, "cw-ghcr-io")
	}
	if remoteURL != "https://ghcr.io" {
		t.Fatalf("remoteURL = %q, want %q", remoteURL, "https://ghcr.io")
	}
	if remoteImage != "cw-ghcr-io:codewiresh/full:latest" {
		t.Fatalf("remoteImage = %q, want %q", remoteImage, "cw-ghcr-io:codewiresh/full:latest")
	}
}

func TestResolveLocalInstanceUsesRepoMatch(t *testing.T) {
	origGetwd := localGetwd
	t.Cleanup(func() { localGetwd = origGetwd })
	localGetwd = func() (string, error) {
		return "/tmp/work/repo", nil
	}

	state := &cwconfig.LocalInstancesConfig{
		Instances: map[string]cwconfig.LocalInstance{
			"repo": {
				Name:        "repo",
				RuntimeName: "cw-repo",
				RepoPath:    "/tmp/work/repo",
			},
		},
	}

	key, instance, err := resolveLocalInstance(state, "")
	if err != nil {
		t.Fatalf("resolveLocalInstance() error = %v", err)
	}
	if key != "repo" {
		t.Fatalf("key = %q, want %q", key, "repo")
	}
	if instance.Name != "repo" {
		t.Fatalf("instance.Name = %q, want %q", instance.Name, "repo")
	}
}

func TestPrepareLocalInstanceUsesCodewireYAMLAndOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "codewire.yaml")
	err := cwconfig.WriteCodewireConfig(cfgPath, &cwconfig.CodewireConfig{
		Preset:  "full",
		Image:   "ghcr.io/codewiresh/full:latest",
		Install: "pnpm install",
		Startup: "pnpm dev",
		CPU:     1000,
	})
	if err != nil {
		t.Fatalf("WriteCodewireConfig() error = %v", err)
	}

	instance, err := prepareLocalInstance(localCreateOptions{
		Backend: "incus",
		Path:    tmpDir,
		File:    "codewire.yaml",
		Image:   "ghcr.io/codewiresh/base:latest",
		Memory:  4096,
	})
	if err != nil {
		t.Fatalf("prepareLocalInstance() error = %v", err)
	}
	if instance.Image != "ghcr.io/codewiresh/base:latest" {
		t.Fatalf("instance.Image = %q, want override image", instance.Image)
	}
	if instance.Install != "pnpm install" {
		t.Fatalf("instance.Install = %q, want %q", instance.Install, "pnpm install")
	}
	if instance.Memory != 4096 {
		t.Fatalf("instance.Memory = %d, want 4096", instance.Memory)
	}
}

func TestCreateLocalIncusInstanceInvokesExpectedCommands(t *testing.T) {
	origLookPath := localLookPath
	origRunCommand := localRunCommand
	t.Cleanup(func() {
		localLookPath = origLookPath
		localRunCommand = origRunCommand
	})

	localLookPath = func(file string) (string, error) {
		if file != "incus" && file != "skopeo" {
			t.Fatalf("LookPath(%q) unexpected", file)
		}
		return "/usr/bin/" + file, nil
	}

	var calls [][]string
	localRunCommand = func(name string, args ...string) ([]byte, error) {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		return nil, nil
	}

	instance := &cwconfig.LocalInstance{
		Name:        "repo",
		Backend:     "incus",
		RuntimeName: "cw-repo",
		RepoPath:    "/tmp/repo",
		Image:       "ghcr.io/codewiresh/full:latest",
		CPU:         1500,
		Memory:      4096,
		Disk:        20,
	}
	if err := createLocalIncusInstance(instance); err != nil {
		t.Fatalf("createLocalIncusInstance() error = %v", err)
	}

	want := [][]string{
		{"incus", "remote", "add", "cw-ghcr-io", "https://ghcr.io", "--protocol=oci"},
		{"incus", "init", "cw-ghcr-io:codewiresh/full:latest", "cw-repo"},
		{"incus", "config", "set", "cw-repo", "limits.cpu", "2"},
		{"incus", "config", "set", "cw-repo", "limits.memory", "4096MiB"},
		{"incus", "config", "device", "set", "cw-repo", "root", "size", "20GiB"},
		{"incus", "config", "device", "add", "cw-repo", "workspace", "disk", "source=/tmp/repo", "path=/workspace"},
		{"incus", "start", "cw-repo"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("incus calls = %#v, want %#v", calls, want)
	}
}

func TestCreateLocalIncusInstanceCleansUpOnFailure(t *testing.T) {
	origLookPath := localLookPath
	origRunCommand := localRunCommand
	t.Cleanup(func() {
		localLookPath = origLookPath
		localRunCommand = origRunCommand
	})

	localLookPath = func(file string) (string, error) {
		if file != "incus" && file != "skopeo" {
			t.Fatalf("LookPath(%q) unexpected", file)
		}
		return "/usr/bin/" + file, nil
	}

	var calls [][]string
	localRunCommand = func(name string, args ...string) ([]byte, error) {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		if reflect.DeepEqual(call, []string{"incus", "start", "cw-repo"}) {
			return []byte("start failed"), errors.New("boom")
		}
		return nil, nil
	}

	instance := &cwconfig.LocalInstance{
		Name:        "repo",
		Backend:     "incus",
		RuntimeName: "cw-repo",
		RepoPath:    "/tmp/repo",
		Image:       "ghcr.io/codewiresh/full:latest",
	}
	err := createLocalIncusInstance(instance)
	if err == nil {
		t.Fatal("expected createLocalIncusInstance() to fail")
	}

	if len(calls) == 0 {
		t.Fatal("expected incus calls")
	}
	gotLast := calls[len(calls)-1]
	wantLast := []string{"incus", "delete", "cw-repo", "--force"}
	if !reflect.DeepEqual(gotLast, wantLast) {
		t.Fatalf("last call = %#v, want %#v", gotLast, wantLast)
	}
}

func TestCreateLocalIncusInstanceRequiresSkopeo(t *testing.T) {
	origLookPath := localLookPath
	t.Cleanup(func() { localLookPath = origLookPath })

	localLookPath = func(file string) (string, error) {
		switch file {
		case "incus":
			return "/usr/bin/incus", nil
		case "skopeo":
			return "", errors.New("not found")
		default:
			t.Fatalf("LookPath(%q) unexpected", file)
			return "", nil
		}
	}

	instance := &cwconfig.LocalInstance{
		Name:        "repo",
		Backend:     "incus",
		RuntimeName: "cw-repo",
		RepoPath:    "/tmp/repo",
		Image:       "ghcr.io/codewiresh/full:latest",
	}
	err := createLocalIncusInstance(instance)
	if err == nil {
		t.Fatal("expected createLocalIncusInstance() to fail")
	}
	if got := err.Error(); got != "skopeo is required for the incus backend when using OCI images: not found" {
		t.Fatalf("error = %q, want skopeo prerequisite error", got)
	}
}

func TestEnsureIncusOCIRemoteIgnoresExistingRemote(t *testing.T) {
	origRunCommand := localRunCommand
	t.Cleanup(func() { localRunCommand = origRunCommand })

	localRunCommand = func(name string, args ...string) ([]byte, error) {
		return []byte("Remote already exists"), errors.New("exists")
	}

	if err := ensureIncusOCIRemote("cw-ghcr-io", "https://ghcr.io"); err != nil {
		t.Fatalf("ensureIncusOCIRemote() error = %v", err)
	}
}

func TestIncusInstanceStatusParsesJSON(t *testing.T) {
	origRunCommand := localRunCommand
	t.Cleanup(func() { localRunCommand = origRunCommand })

	localRunCommand = func(name string, args ...string) ([]byte, error) {
		return []byte(`[{"status":"Running"}]`), nil
	}

	got, err := incusInstanceStatus("cw-repo")
	if err != nil {
		t.Fatalf("incusInstanceStatus() error = %v", err)
	}
	if got != "running" {
		t.Fatalf("status = %q, want %q", got, "running")
	}
}

func TestIncusInstanceStatusMissingOnNotFound(t *testing.T) {
	origRunCommand := localRunCommand
	t.Cleanup(func() { localRunCommand = origRunCommand })

	localRunCommand = func(name string, args ...string) ([]byte, error) {
		return []byte("Instance not found"), errors.New("missing")
	}

	got, err := incusInstanceStatus("cw-repo")
	if err != nil {
		t.Fatalf("incusInstanceStatus() error = %v", err)
	}
	if got != "missing" {
		t.Fatalf("status = %q, want %q", got, "missing")
	}
}

func TestFormatDockerCPUs(t *testing.T) {
	if got := formatDockerCPUs(1500); got != "1.500" {
		t.Fatalf("formatDockerCPUs(1500) = %q, want %q", got, "1.500")
	}
	if got := formatDockerCPUs(2000); got != "2" {
		t.Fatalf("formatDockerCPUs(2000) = %q, want %q", got, "2")
	}
}

func TestCreateLocalDockerInstanceInvokesExpectedCommands(t *testing.T) {
	origLookPath := localLookPath
	origRunCommand := localRunCommand
	t.Cleanup(func() {
		localLookPath = origLookPath
		localRunCommand = origRunCommand
	})

	localLookPath = func(file string) (string, error) {
		if file != "docker" {
			t.Fatalf("LookPath(%q) unexpected", file)
		}
		return "/usr/bin/docker", nil
	}

	var calls [][]string
	localRunCommand = func(name string, args ...string) ([]byte, error) {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		return nil, nil
	}

	instance := &cwconfig.LocalInstance{
		Name:        "repo",
		Backend:     "docker",
		RuntimeName: "cw-repo",
		RepoPath:    "/tmp/repo",
		Image:       "ghcr.io/codewiresh/full:latest",
		CPU:         1500,
		Memory:      4096,
		Env: map[string]string{
			"B": "2",
			"A": "1",
		},
	}
	if err := createLocalDockerInstance(instance); err != nil {
		t.Fatalf("createLocalDockerInstance() error = %v", err)
	}

	want := [][]string{
		{"docker", "create", "--name", "cw-repo", "--hostname", "cw-repo", "--workdir", "/workspace", "--volume", "/tmp/repo:/workspace", "--cpus", "1.500", "--memory", "4096m", "--env", "A=1", "--env", "B=2", "ghcr.io/codewiresh/full:latest", "/bin/sh", "-lc", "trap 'exit 0' TERM INT; while true; do sleep 3600; done"},
		{"docker", "start", "cw-repo"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("docker calls = %#v, want %#v", calls, want)
	}
}

func TestCreateLocalDockerInstanceCleansUpOnFailure(t *testing.T) {
	origLookPath := localLookPath
	origRunCommand := localRunCommand
	t.Cleanup(func() {
		localLookPath = origLookPath
		localRunCommand = origRunCommand
	})

	localLookPath = func(file string) (string, error) {
		return "/usr/bin/docker", nil
	}

	var calls [][]string
	localRunCommand = func(name string, args ...string) ([]byte, error) {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		if reflect.DeepEqual(call, []string{"docker", "start", "cw-repo"}) {
			return []byte("start failed"), errors.New("boom")
		}
		return nil, nil
	}

	instance := &cwconfig.LocalInstance{
		Name:        "repo",
		Backend:     "docker",
		RuntimeName: "cw-repo",
		RepoPath:    "/tmp/repo",
		Image:       "ghcr.io/codewiresh/full:latest",
	}
	err := createLocalDockerInstance(instance)
	if err == nil {
		t.Fatal("expected createLocalDockerInstance() to fail")
	}

	gotLast := calls[len(calls)-1]
	wantLast := []string{"docker", "rm", "-f", "cw-repo"}
	if !reflect.DeepEqual(gotLast, wantLast) {
		t.Fatalf("last call = %#v, want %#v", gotLast, wantLast)
	}
}

func TestLimaCreateCommandArgs(t *testing.T) {
	origGOOS := localGOOS
	t.Cleanup(func() { localGOOS = origGOOS })
	localGOOS = "linux"

	instance := &cwconfig.LocalInstance{
		Name:        "repo",
		RuntimeName: "cw-repo",
		RepoPath:    "/tmp/repo",
		CPU:         1500,
		Memory:      4096,
		Disk:        20,
		Ports: []cwconfig.PortConfig{
			{Port: 3000, Label: "web"},
			{Port: 8080, Label: "api"},
		},
	}

	got := limaCreateCommandArgs(instance)
	want := []string{
		"start",
		"--tty=false",
		"--name", "cw-repo",
		"--vm-type", "qemu",
		"--mount-type", "9p",
		"--mount-none",
		"--set", `.mounts=[{"location":"/tmp/repo","mountPoint":"/workspace","writable":true}]`,
		"--cpus", "2",
		"--memory", "4",
		"--disk", "20",
		"--port-forward", "3000:3000,static=true",
		"--port-forward", "8080:8080,static=true",
		"template://default",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lima args = %#v, want %#v", got, want)
	}
}

func TestCreateLocalLimaInstanceInvokesExpectedCommands(t *testing.T) {
	origLookPath := localLookPath
	origRunCommand := localRunCommand
	origGOOS := localGOOS
	t.Cleanup(func() {
		localLookPath = origLookPath
		localRunCommand = origRunCommand
		localGOOS = origGOOS
	})

	localGOOS = "linux"
	localLookPath = func(file string) (string, error) {
		if file != "limactl" {
			t.Fatalf("LookPath(%q) unexpected", file)
		}
		return "/usr/bin/limactl", nil
	}

	var calls [][]string
	localRunCommand = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}

	instance := &cwconfig.LocalInstance{
		Name:        "repo",
		Backend:     "lima",
		RuntimeName: "cw-repo",
		RepoPath:    "/tmp/repo",
		CPU:         1500,
		Memory:      4096,
		Disk:        20,
		Ports: []cwconfig.PortConfig{
			{Port: 3000, Label: "web"},
		},
	}
	if err := createLocalLimaInstance(instance); err != nil {
		t.Fatalf("createLocalLimaInstance() error = %v", err)
	}

	want := [][]string{append([]string{"limactl"}, limaCreateCommandArgs(instance)...)}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("lima calls = %#v, want %#v", calls, want)
	}
	if instance.LimaInstanceName != "cw-repo" {
		t.Fatalf("LimaInstanceName = %q, want %q", instance.LimaInstanceName, "cw-repo")
	}
	if instance.LimaVMType != "qemu" {
		t.Fatalf("LimaVMType = %q, want qemu", instance.LimaVMType)
	}
	if instance.LimaMountType != "9p" {
		t.Fatalf("LimaMountType = %q, want 9p", instance.LimaMountType)
	}
}

func TestLimaLifecycleCommands(t *testing.T) {
	origLookPath := localLookPath
	origRunCommand := localRunCommand
	t.Cleanup(func() {
		localLookPath = origLookPath
		localRunCommand = origRunCommand
	})

	localLookPath = func(file string) (string, error) {
		if file != "limactl" {
			t.Fatalf("LookPath(%q) unexpected", file)
		}
		return "/usr/bin/limactl", nil
	}

	var calls [][]string
	localRunCommand = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}

	instance := &cwconfig.LocalInstance{
		Name:             "repo",
		Backend:          "lima",
		LimaInstanceName: "cw-repo",
	}
	if err := startLocalLimaInstance(instance); err != nil {
		t.Fatalf("startLocalLimaInstance() error = %v", err)
	}
	if err := stopLocalLimaInstance(instance); err != nil {
		t.Fatalf("stopLocalLimaInstance() error = %v", err)
	}
	if err := deleteLocalLimaInstance(instance); err != nil {
		t.Fatalf("deleteLocalLimaInstance() error = %v", err)
	}

	want := [][]string{
		{"limactl", "start", "--tty=false", "cw-repo"},
		{"limactl", "stop", "cw-repo"},
		{"limactl", "delete", "--force", "cw-repo"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("lima calls = %#v, want %#v", calls, want)
	}
}

func TestLimaInstanceStatusParsesListOutput(t *testing.T) {
	origLookPath := localLookPath
	origRunCommand := localRunCommand
	t.Cleanup(func() {
		localLookPath = origLookPath
		localRunCommand = origRunCommand
	})

	localLookPath = func(file string) (string, error) {
		if file != "limactl" {
			t.Fatalf("LookPath(%q) unexpected", file)
		}
		return "/usr/bin/limactl", nil
	}
	localRunCommand = func(name string, args ...string) ([]byte, error) {
		return []byte(`[{"name":"cw-repo","status":"Running"}]`), nil
	}

	got, err := limaInstanceStatus(&cwconfig.LocalInstance{LimaInstanceName: "cw-repo"})
	if err != nil {
		t.Fatalf("limaInstanceStatus() error = %v", err)
	}
	if got != "running" {
		t.Fatalf("status = %q, want %q", got, "running")
	}
}

func TestLocalPortSummaryFormatsLimaPorts(t *testing.T) {
	got := localPortSummary(&cwconfig.LocalInstance{
		Backend: "lima",
		Ports: []cwconfig.PortConfig{
			{Port: 3000, Label: "web"},
			{Port: 8080, Label: "api"},
		},
	})
	want := "3000 -> 3000 (web), 8080 -> 8080 (api)"
	if got != want {
		t.Fatalf("localPortSummary() = %q, want %q", got, want)
	}
}

func TestLocalInfoCmdPrintsLimaPortSummary(t *testing.T) {
	origLoadLocal := loadLocalInstancesForCLI
	origLookPath := localLookPath
	origRunCommand := localRunCommand
	t.Cleanup(func() {
		loadLocalInstancesForCLI = origLoadLocal
		localLookPath = origLookPath
		localRunCommand = origRunCommand
	})

	loadLocalInstancesForCLI = func() (*cwconfig.LocalInstancesConfig, error) {
		return &cwconfig.LocalInstancesConfig{
			Instances: map[string]cwconfig.LocalInstance{
				"repo": {
					Name:             "repo",
					Backend:          "lima",
					RuntimeName:      "cw-repo",
					RepoPath:         "/tmp/repo",
					Workdir:          "/workspace",
					Image:            "ghcr.io/codewiresh/full:latest",
					LimaInstanceName: "cw-repo",
					LimaVMType:       "qemu",
					LimaMountType:    "9p",
					Ports: []cwconfig.PortConfig{
						{Port: 3000, Label: "web"},
					},
				},
			},
		}, nil
	}
	localLookPath = func(file string) (string, error) {
		if file != "limactl" {
			t.Fatalf("LookPath(%q) unexpected", file)
		}
		return "/usr/bin/limactl", nil
	}
	localRunCommand = func(name string, args ...string) ([]byte, error) {
		return []byte(`[{"name":"cw-repo","status":"Running"}]`), nil
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	cmd := localInfoCmd()
	cmd.SetArgs([]string{"repo"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("local info command failed: %v", err)
	}

	_ = w.Close()
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	got := string(output)
	if !strings.Contains(got, "Ports:") {
		t.Fatalf("expected Ports line, got %q", got)
	}
	if !strings.Contains(got, "3000 -> 3000 (web)") {
		t.Fatalf("expected lima port mapping, got %q", got)
	}
}

func TestLocalListCmdPrintsPortColumn(t *testing.T) {
	origLoadLocal := loadLocalInstancesForCLI
	origLookPath := localLookPath
	origRunCommand := localRunCommand
	t.Cleanup(func() {
		loadLocalInstancesForCLI = origLoadLocal
		localLookPath = origLookPath
		localRunCommand = origRunCommand
	})

	loadLocalInstancesForCLI = func() (*cwconfig.LocalInstancesConfig, error) {
		return &cwconfig.LocalInstancesConfig{
			Instances: map[string]cwconfig.LocalInstance{
				"repo": {
					Name:             "repo",
					Backend:          "lima",
					RuntimeName:      "cw-repo",
					RepoPath:         "/tmp/repo",
					Image:            "ghcr.io/codewiresh/full:latest",
					LimaInstanceName: "cw-repo",
					Ports: []cwconfig.PortConfig{
						{Port: 3000, Label: "web"},
					},
				},
			},
		}, nil
	}
	localLookPath = func(file string) (string, error) {
		if file != "limactl" {
			t.Fatalf("LookPath(%q) unexpected", file)
		}
		return "/usr/bin/limactl", nil
	}
	localRunCommand = func(name string, args ...string) ([]byte, error) {
		return []byte(`[{"name":"cw-repo","status":"Running"}]`), nil
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	cmd := localListCmd()
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("local list command failed: %v", err)
	}

	_ = w.Close()
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	got := string(output)
	if !strings.Contains(got, "PORTS") {
		t.Fatalf("expected PORTS header, got %q", got)
	}
	if !strings.Contains(got, "3000 -> 3000 (web)") {
		t.Fatalf("expected lima port mapping, got %q", got)
	}
}

func TestDockerContainerStatusParsesInspectOutput(t *testing.T) {
	origRunCommand := localRunCommand
	t.Cleanup(func() { localRunCommand = origRunCommand })

	localRunCommand = func(name string, args ...string) ([]byte, error) {
		return []byte("running\n"), nil
	}

	got, err := dockerContainerStatus("cw-repo")
	if err != nil {
		t.Fatalf("dockerContainerStatus() error = %v", err)
	}
	if got != "running" {
		t.Fatalf("status = %q, want %q", got, "running")
	}
}

func TestDockerContainerStatusMissingOnNotFound(t *testing.T) {
	origRunCommand := localRunCommand
	t.Cleanup(func() { localRunCommand = origRunCommand })

	localRunCommand = func(name string, args ...string) ([]byte, error) {
		return []byte("Error: No such container"), errors.New("missing")
	}

	got, err := dockerContainerStatus("cw-repo")
	if err != nil {
		t.Fatalf("dockerContainerStatus() error = %v", err)
	}
	if got != "missing" {
		t.Fatalf("status = %q, want %q", got, "missing")
	}
}
