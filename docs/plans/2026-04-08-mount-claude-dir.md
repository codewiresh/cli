# Mount ~/.claude into Local Backends Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Mount the host's `~/.claude` directory read-write into all local backends so Claude Code inside containers has the same settings, history, plugins, and projects as the host.

**Architecture:** Each local backend (Docker, Lima, Incus) adds a second volume mount for `~/.claude` -> `/home/codewire/.claude` alongside the existing workspace mount. The container user is `codewire` (UID 1000) with home at `/home/codewire`.

**Tech Stack:** Go, cobra, Docker CLI, limactl, incus CLI

---

### Task 1: Docker backend -- mount ~/.claude

**Files:**
- Modify: `cmd/cw/local_cmd.go:727-755`
- Test: `cmd/cw/local_cmd_test.go`

**Step 1: Write the failing test**

Add a test that verifies the docker create args include the ~/.claude volume mount:

```go
func TestCreateLocalDockerInstanceMountsClaudeDir(t *testing.T) {
	// ... mock localLookPath, localRunCommand ...
	// Create instance, verify "--volume", "<home>/.claude:/home/codewire/.claude"
	// appears in the docker create args
}
```

**Step 2: Run test to verify it fails**

Run: `TMPDIR=/home/noel/tmp go test ./cmd/cw/ -run TestCreateLocalDockerInstanceMountsClaudeDir -v`
Expected: FAIL

**Step 3: Add the mount to createLocalDockerInstance**

In `local_cmd.go`, inside `createLocalDockerInstance`, after the workspace volume line:

```go
args := []string{
    "create",
    "--name", instance.RuntimeName,
    "--hostname", instance.RuntimeName,
    "--workdir", localWorkspacePath,
    "--volume", instance.RepoPath + ":" + localWorkspacePath,
}
// Add ~/.claude mount
if homeDir, err := os.UserHomeDir(); err == nil {
    claudeDir := filepath.Join(homeDir, ".claude")
    if _, statErr := os.Stat(claudeDir); statErr == nil {
        args = append(args, "--volume", claudeDir+":/home/codewire/.claude")
    }
}
```

**Step 4: Run test to verify it passes**

Run: `TMPDIR=/home/noel/tmp go test ./cmd/cw/ -run TestCreateLocalDockerInstanceMountsClaudeDir -v`
Expected: PASS

**Step 5: Commit**

```bash
git add cmd/cw/local_cmd.go cmd/cw/local_cmd_test.go
git commit -m "feat: mount ~/.claude into docker backend containers"
```

---

### Task 2: Lima backend -- mount ~/.claude into VM and container

**Files:**
- Modify: `cmd/cw/lima_backend.go:162-166` (VM mount set)
- Modify: `cmd/cw/lima_backend.go:259-266` (docker run inside VM)
- Test: `cmd/cw/local_cmd_test.go`

**Step 1: Write the failing test**

Update `TestLimaCreateCommandArgs` to expect the ~/.claude mount in the `--set .mounts=[...]` array.

Update `TestCreateLocalLimaInstanceInvokesExpectedCommands` to expect `-v <home>/.claude:/home/codewire/.claude` in the docker run args.

**Step 2: Run tests to verify they fail**

Run: `TMPDIR=/home/noel/tmp go test ./cmd/cw/ -run "TestLimaCreateCommandArgs|TestCreateLocalLimaInstance" -v`
Expected: FAIL

**Step 3: Add mounts**

In `limaCreateCommandArgs`, add `~/.claude` to the VM mount set (alongside workspace and gh config):

```go
claudeDir := filepath.Join(homeDir, ".claude")
mountSet := fmt.Sprintf(
    `.mounts=[{"location":%s,"mountPoint":"/workspace","writable":true},{"location":%s,"mountPoint":"/home/{{.User}}.guest/.config/gh","writable":false},{"location":%s,"mountPoint":"/home/{{.User}}.guest/.claude","writable":true}]`,
    strconv.Quote(instance.RepoPath),
    strconv.Quote(ghConfigDir),
    strconv.Quote(claudeDir),
)
```

In `createLocalLimaInstance`, add the volume to the docker run command:

```go
// Mount ~/.claude from VM into the container
homeDir, _ := os.UserHomeDir()
claudeMount := filepath.Join("/home", os.Getenv("USER")+".guest", ".claude") + ":/home/codewire/.claude"
```

Add `-v claudeMount` to the docker run args.

**Step 4: Run tests to verify they pass**

**Step 5: Commit**

```bash
git add cmd/cw/lima_backend.go cmd/cw/local_cmd_test.go
git commit -m "feat: mount ~/.claude into lima backend VM and container"
```

---

### Task 3: Incus backend -- mount ~/.claude

**Files:**
- Modify: `cmd/cw/local_cmd.go:666-720`
- Test: `cmd/cw/local_cmd_test.go`

**Step 1: Write the failing test**

Verify that `createLocalIncusInstance` includes an incus config device add for ~/.claude.

**Step 2: Run test to verify it fails**

**Step 3: Add the mount**

After the workspace device add, add:

```go
if homeDir, err := os.UserHomeDir(); err == nil {
    claudeDir := filepath.Join(homeDir, ".claude")
    if _, statErr := os.Stat(claudeDir); statErr == nil {
        if err := runIncus("config", "device", "add", instance.RuntimeName, "claude-config", "disk",
            "source="+claudeDir, "path=/home/codewire/.claude"); err != nil {
            return err
        }
    }
}
```

**Step 4: Run tests to verify they pass**

**Step 5: Commit**

```bash
git add cmd/cw/local_cmd.go cmd/cw/local_cmd_test.go
git commit -m "feat: mount ~/.claude into incus backend containers"
```

---

### Task 4: Run full test suite and verify

**Step 1: Run all tests**

```bash
TMPDIR=/home/noel/tmp go test ./cmd/cw/ -timeout 30s -count=1
```

**Step 2: Run vet**

```bash
TMPDIR=/home/noel/tmp go vet ./cmd/cw/
```

**Step 3: Build and install**

```bash
TMPDIR=/home/noel/tmp go build -ldflags="-X main.version=v0.3.13-dirty" -o cw ./cmd/cw
sudo rm -f /usr/local/bin/cw && sudo cp cw /usr/local/bin/cw
```

**Step 4: Manual verification (Docker)**

```bash
cw local create --backend docker --name test-claude-mount
cw exec --on test-claude-mount -- ls -la /home/codewire/.claude/
cw local rm test-claude-mount
```

**Step 5: Manual verification (Lima)**

```bash
cw exec --on mempalace -- ls -la /home/codewire/.claude/
```

**Step 6: Commit if any fixes needed**
