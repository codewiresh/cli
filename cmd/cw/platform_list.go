package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/codewiresh/codewire/internal/client"
	"github.com/codewiresh/codewire/internal/platform"
	"github.com/codewiresh/codewire/internal/protocol"
)

type platformListEntry struct {
	Environment   platform.Environment   `json:"environment"`
	Sessions      []protocol.SessionInfo `json:"sessions,omitempty"`
	SessionLookup string                 `json:"session_lookup,omitempty"`
	SessionError  string                 `json:"session_error,omitempty"`
}

func platformListCmd() *cobra.Command {
	var jsonOutput bool
	var statusFilter string
	var localOnly bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List environments and runs",
		Long: "In platform mode: show environments in the current org and runs inside running sandbox environments.\n" +
			"In standalone mode: list local sessions.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if localOnly || !platform.HasConfig() {
				target, err := resolveTarget()
				if err != nil {
					return err
				}
				if target.IsLocal() {
					if err := ensureNode(); err != nil {
						return err
					}
				}
				return client.List(target, jsonOutput, statusFilter)
			}

			orgID, pc, err := getOrgContext(cmd)
			if err != nil {
				return err
			}

			envs, err := pc.ListEnvironments(orgID, "", "", false)
			if err != nil {
				return fmt.Errorf("list environments: %w", err)
			}

			entries := listPlatformEntries(pc, orgID, envs)

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Environments []platformListEntry `json:"environments"`
				}{
					Environments: entries,
				})
			}

			if len(entries) == 0 {
				fmt.Println("No environments.")
				return nil
			}

			return printPlatformEntries(entries)
		},
	}

	cmd.Flags().BoolVarP(&jsonOutput, "json", "j", false, "Output as JSON")
	cmd.Flags().BoolVar(&localOnly, "local", false, "Force local session listing even when platform config exists")
	cmd.Flags().String("org", "", "Organization ID or slug (default: current org)")
	cmd.Flags().StringVar(&statusFilter, "status", "all", "Filter by status (standalone mode): all, running, completed, killed")
	_ = cmd.RegisterFlagCompletionFunc("status", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"all", "running", "completed", "killed"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func listPlatformEntries(pc *platform.Client, orgID string, envs []platform.Environment) []platformListEntry {
	entries := make([]platformListEntry, 0, len(envs))
	for _, env := range envs {
		entry := platformListEntry{Environment: env}
		sessions, lookup, errMsg := listEnvironmentRuns(pc, orgID, env)
		entry.Sessions = sessions
		entry.SessionLookup = lookup
		entry.SessionError = errMsg
		entries = append(entries, entry)
	}
	return entries
}

func listEnvironmentRuns(pc *platform.Client, orgID string, env platform.Environment) ([]protocol.SessionInfo, string, string) {
	if env.Type != "sandbox" {
		return nil, "unsupported", ""
	}
	if env.State != "running" {
		return nil, "skipped", ""
	}

	result, err := pc.ExecInEnvironment(orgID, env.ID, &platform.ExecRequest{
		Command:    []string{"cw", "list", "--local", "--json"},
		WorkingDir: "/workspace",
		Timeout:    10,
	})
	if err != nil {
		return nil, "unavailable", err.Error()
	}
	if result.ExitCode != 0 {
		return nil, "unavailable", summarizeExecError(result)
	}

	var sessions []protocol.SessionInfo
	if err := json.Unmarshal([]byte(result.Stdout), &sessions); err != nil {
		return nil, "unavailable", fmt.Sprintf("decode runs: %v", err)
	}
	return sessions, "available", ""
}

func summarizeExecError(result *platform.ExecResult) string {
	msg := strings.TrimSpace(result.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(result.Stdout)
	}
	if msg == "" {
		msg = fmt.Sprintf("cw list exited with code %d", result.ExitCode)
	}
	msg = strings.ReplaceAll(msg, "\n", " ")
	if len(msg) > 96 {
		msg = msg[:93] + "..."
	}
	return msg
}

func printPlatformEntries(entries []platformListEntry) error {
	for _, entry := range entries {
		env := entry.Environment
		envName := env.ID
		if env.Name != nil && strings.TrimSpace(*env.Name) != "" {
			envName = *env.Name
		}

		runSummary := "n/a"
		switch entry.SessionLookup {
		case "available":
			runSummary = fmt.Sprintf("%d", len(entry.Sessions))
		case "unavailable":
			runSummary = "?"
		}

		fmt.Printf("%s (%s)\n", bold(envName), dim(env.ID))
		fmt.Printf("  state: %s  type: %s  size: %dm/%dMB  runs: %s  created: %s\n",
			stateColor(env.State), env.Type, env.CPUMillicores, env.MemoryMB, runSummary, timeAgo(env.CreatedAt))

		if entry.SessionLookup == "unavailable" && entry.SessionError != "" {
			fmt.Printf("  runs: unavailable (%s)\n", entry.SessionError)
		}

		if entry.SessionLookup == "available" {
			if len(entry.Sessions) == 0 {
				fmt.Println("  runs: none")
			} else {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				tableHeader(w, "  ID", "NAME", "STATUS", "AGE", "COMMAND")
				for _, session := range entry.Sessions {
					name := session.Name
					if name == "" {
						name = "-"
					}
					fmt.Fprintf(w, "  %d\t%s\t%s\t%s\t%s\n",
						session.ID,
						name,
						stateColor(session.Status),
						timeAgo(session.CreatedAt),
						truncateRunCommand(session.Prompt),
					)
				}
				if err := w.Flush(); err != nil {
					return err
				}
			}
		}

		fmt.Println()
	}
	return nil
}

func truncateRunCommand(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "-"
	}
	if len(prompt) > 60 {
		return prompt[:57] + "..."
	}
	return prompt
}
