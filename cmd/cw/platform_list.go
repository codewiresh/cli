package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/codewiresh/codewire/internal/client"
	"github.com/codewiresh/codewire/internal/platform"
)

func platformListCmd() *cobra.Command {
	var jsonOutput bool
	var statusFilter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List environments and sessions",
		Long:  "In platform mode: show environments in the current org.\nIn standalone mode: list local sessions.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// If not in platform mode, fall back to local session list
			if !platform.HasConfig() {
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

			orgID, pc, err := getDefaultOrg()
			if err != nil {
				return err
			}

			envs, err := pc.ListEnvironments(orgID, "", "", false)
			if err != nil {
				return fmt.Errorf("list environments: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(envs)
			}

			if len(envs) == 0 {
				fmt.Println("No environments.")
				return nil
			}

			for _, env := range envs {
				envName := env.ID
				if env.Name != nil {
					envName = *env.Name
				}
				fmt.Printf("  %-24s %-10s %-10s %dm / %dMB\n",
					envName, stateColor(env.State), env.Type,
					env.CPUMillicores, env.MemoryMB)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&jsonOutput, "json", "j", false, "Output as JSON")
	cmd.Flags().StringVar(&statusFilter, "status", "all", "Filter by status (standalone mode): all, running, completed, killed")
	_ = cmd.RegisterFlagCompletionFunc("status", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"all", "running", "completed", "killed"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}
