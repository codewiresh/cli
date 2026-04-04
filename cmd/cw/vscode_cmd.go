package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func vscodeCmd() *cobra.Command {
	var folder string

	cmd := &cobra.Command{
		Use:   "vscode [env-id-or-name]",
		Short: "Open a running environment in VS Code",
		Long: `Open VS Code connected to a running environment via Remote-SSH.

Ensures your SSH config is up to date (same as 'cw config-ssh'),
then launches VS Code with the vscode:// URI scheme.

Requires:
  - VS Code installed locally
  - Remote-SSH extension installed in VS Code`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: envCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := ""
			if len(args) > 0 {
				ref = args[0]
				if strings.HasPrefix(ref, "cw-") {
					ref = ref[3:]
				}
			}

			target, err := requireEnvironmentTarget(ref)
			if err != nil {
				return err
			}
			envID := target.Ref

			// Ensure SSH config is up to date.
			if err := writeSSHConfig(); err != nil {
				return fmt.Errorf("update ssh config: %w", err)
			}

			uri := fmt.Sprintf("vscode://vscode-remote/ssh-remote+cw-%s/%s", envID, folder)
			fmt.Fprintf(cmd.OutOrStdout(), "Opening VS Code for environment %s...\n", envID)
			return openBrowser(uri)
		},
	}

	cmd.Flags().StringVar(&folder, "folder", "/workspace", "Remote folder to open")
	cmd.Flags().String("org", "", "Organization ID or slug (default: current org)")
	return cmd
}
