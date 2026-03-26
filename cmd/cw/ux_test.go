package main

import (
	"io"
	"os"
	"strings"
	"testing"

	cwconfig "github.com/codewiresh/codewire/internal/config"
)

func TestEnvParentCmdHasOrgFlag(t *testing.T) {
	cmd := envParentCmd()
	if cmd.PersistentFlags().Lookup("org") == nil {
		t.Fatal("expected env command to expose a persistent --org flag")
	}
}

func TestSSHCmdReferencesConfigSSH(t *testing.T) {
	cmd := sshCmd()
	if !strings.Contains(cmd.Long, "cw config-ssh") {
		t.Fatalf("expected ssh help to reference cw config-ssh, got %q", cmd.Long)
	}
}

func TestOrgCommandShape(t *testing.T) {
	cmd := orgsCmd()
	if cmd.Use != "org" {
		t.Fatalf("expected Use to be org, got %q", cmd.Use)
	}
	if cmd.RunE == nil {
		t.Fatal("expected bare org command to have a default action")
	}

	foundAlias := false
	for _, alias := range cmd.Aliases {
		if alias == "orgs" {
			foundAlias = true
			break
		}
	}
	if !foundAlias {
		t.Fatal("expected org command to keep orgs alias")
	}

	for _, sub := range cmd.Commands() {
		if sub.Name() == "set" {
			if err := sub.Args(sub, nil); err != nil {
				t.Fatalf("expected org set to allow zero args, got %v", err)
			}
			return
		}
	}

	t.Fatal("expected org command to include a set subcommand")
}

func TestResourcesCommandShape(t *testing.T) {
	cmd := resourcesCmd()
	if cmd.RunE == nil {
		t.Fatal("expected bare resources command to have a default action")
	}

	subcommands := map[string]bool{}
	for _, sub := range cmd.Commands() {
		subcommands[sub.Name()] = true
	}

	if !subcommands["list"] {
		t.Fatal("expected resources command to include list")
	}
	if subcommands["create"] || subcommands["delete"] || subcommands["get"] || subcommands["status"] {
		t.Fatalf("expected resources command to stay read-only, got subcommands: %#v", subcommands)
	}
}

func TestNetworkCommandShape(t *testing.T) {
	cmd := networkCmd()

	subcommands := map[string]bool{}
	for _, sub := range cmd.Commands() {
		subcommands[sub.Name()] = true
	}

	for _, required := range []string{"list", "create", "current", "use", "nodes", "invite", "revoke"} {
		if !subcommands[required] {
			t.Fatalf("expected network command to include %q, got %#v", required, subcommands)
		}
	}
}

func TestCurrentNetworkCmdPrintsSelectedNetwork(t *testing.T) {
	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("Setenv HOME: %v", err)
	}
	defer func() { _ = os.Setenv("HOME", oldHome) }()

	network := "project-alpha"
	if err := cwconfig.SaveConfig(dataDir(), &cwconfig.Config{RelayNetwork: &network}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	cmd := currentNetworkCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("current network command failed: %v", err)
	}

	_ = w.Close()
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if strings.TrimSpace(string(output)) != "project-alpha" {
		t.Fatalf("unexpected output %q", string(output))
	}
}

func TestNodeCommandShape(t *testing.T) {
	cmd := nodeCmd()

	subcommands := map[string]bool{}
	for _, sub := range cmd.Commands() {
		subcommands[sub.Name()] = true
	}

	for _, required := range []string{"stop", "qr", "list"} {
		if !subcommands[required] {
			t.Fatalf("expected node command to include %q, got %#v", required, subcommands)
		}
	}
}

func TestRelayCommandShape(t *testing.T) {
	cmd := relayCmd()

	subcommands := map[string]bool{}
	for _, sub := range cmd.Commands() {
		subcommands[sub.Name()] = true
	}

	for _, required := range []string{"serve", "setup"} {
		if !subcommands[required] {
			t.Fatalf("expected relay command to include %q, got %#v", required, subcommands)
		}
	}
}

func TestMsgCmdRejectsRemoteLocatorUntilPeerTransportIsWired(t *testing.T) {
	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldServer := serverFlag
	oldToken := tokenFlag
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("Setenv HOME: %v", err)
	}
	defer func() {
		_ = os.Setenv("HOME", oldHome)
		serverFlag = oldServer
		tokenFlag = oldToken
	}()
	serverFlag = ""
	tokenFlag = ""

	cmd := msgCmd()
	err := cmd.RunE(cmd, []string{"dev-2:coder", "hello"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not discoverable yet") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMsgCmdRejectsRemoteLocatorWithServerFlag(t *testing.T) {
	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldServer := serverFlag
	oldToken := tokenFlag
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("Setenv HOME: %v", err)
	}
	defer func() {
		_ = os.Setenv("HOME", oldHome)
		serverFlag = oldServer
		tokenFlag = oldToken
	}()
	serverFlag = "http://example.com"
	tokenFlag = ""

	cmd := msgCmd()
	err := cmd.RunE(cmd, []string{"dev-2:coder", "hello"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "cannot be combined with --server") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInboxCmdRejectsRemoteLocatorUntilPeerTransportIsWired(t *testing.T) {
	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldServer := serverFlag
	oldToken := tokenFlag
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("Setenv HOME: %v", err)
	}
	defer func() {
		_ = os.Setenv("HOME", oldHome)
		serverFlag = oldServer
		tokenFlag = oldToken
	}()
	serverFlag = ""
	tokenFlag = ""

	cmd := inboxCmd()
	err := cmd.RunE(cmd, []string{"dev-2:coder"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not discoverable yet") {
		t.Fatalf("unexpected error: %v", err)
	}
}
