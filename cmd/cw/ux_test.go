package main

import (
	"strings"
	"testing"
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

	for _, required := range []string{"list", "create", "use", "nodes", "invite", "revoke"} {
		if !subcommands[required] {
			t.Fatalf("expected network command to include %q, got %#v", required, subcommands)
		}
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
