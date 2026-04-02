package main

import (
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/codewiresh/codewire/internal/platform"
	"github.com/spf13/cobra"
)

func portParentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port",
		Short: "Manage environment ports",
	}
	cmd.AddCommand(portAddCmd())
	cmd.AddCommand(portListCmd())
	cmd.AddCommand(portRmCmd())
	return cmd
}

func portAddCmd() *cobra.Command {
	var label, access, envRef string

	cmd := &cobra.Command{
		Use:   "add <port>",
		Short: "Register a port and get a preview URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[0])
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("invalid port number: %s", args[0])
			}
			if label == "" {
				return fmt.Errorf("--label is required")
			}

			orgID, client, err := getOrgContext(cmd)
			if err != nil {
				return err
			}

			envID, err := resolvePortEnvID(cmd, client, orgID, envRef)
			if err != nil {
				return err
			}

			req := &platform.CreatePortRequest{
				Port:   port,
				Label:  label,
				Access: access,
			}

			result, err := client.CreatePort(orgID, envID, req)
			if err != nil {
				return fmt.Errorf("create port: %w", err)
			}

			fmt.Printf("  %s Port %d registered as %s\n", green("*"), result.Port, bold(result.Label))
			if result.PreviewURL != "" {
				fmt.Printf("  %s %s\n", dim("URL:"), result.PreviewURL)
			}
			fmt.Printf("  %s %s\n", dim("Access:"), result.Access)
			return nil
		},
	}

	cmd.Flags().StringVarP(&label, "label", "l", "", "DNS-safe label for the preview URL (required)")
	cmd.Flags().StringVarP(&access, "access", "a", "creator", "Access level: public, org_members, or creator")
	cmd.Flags().StringVarP(&envRef, "env", "e", "", "Environment ID or name (default: current)")
	_ = cmd.MarkFlagRequired("label")

	return cmd
}

func portListCmd() *cobra.Command {
	var envRef string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered ports",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			orgID, client, err := getOrgContext(cmd)
			if err != nil {
				return err
			}

			envID, err := resolvePortEnvID(cmd, client, orgID, envRef)
			if err != nil {
				return err
			}

			ports, err := client.ListPorts(orgID, envID)
			if err != nil {
				return fmt.Errorf("list ports: %w", err)
			}

			if len(ports) == 0 {
				fmt.Println("No ports registered.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			tableHeader(w, "PORT", "LABEL", "ACCESS", "PREVIEW URL")
			for _, p := range ports {
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", p.Port, p.Label, p.Access, p.PreviewURL)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().StringVarP(&envRef, "env", "e", "", "Environment ID or name (default: current)")
	return cmd
}

func portRmCmd() *cobra.Command {
	var envRef string

	cmd := &cobra.Command{
		Use:   "rm <port-number-or-label>",
		Short: "Remove a registered port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			orgID, client, err := getOrgContext(cmd)
			if err != nil {
				return err
			}

			envID, err := resolvePortEnvID(cmd, client, orgID, envRef)
			if err != nil {
				return err
			}

			// Find the port by number or label.
			ports, err := client.ListPorts(orgID, envID)
			if err != nil {
				return fmt.Errorf("list ports: %w", err)
			}

			ref := args[0]
			var target *platform.EnvironmentPort
			for i, p := range ports {
				if p.Label == ref || strconv.Itoa(p.Port) == ref || p.ID == ref {
					target = &ports[i]
					break
				}
			}
			if target == nil {
				return fmt.Errorf("port %q not found", ref)
			}

			if err := client.DeletePort(orgID, envID, target.ID); err != nil {
				return fmt.Errorf("delete port: %w", err)
			}

			fmt.Printf("  %s Port %d (%s) removed\n", green("*"), target.Port, target.Label)
			return nil
		},
	}

	cmd.Flags().StringVarP(&envRef, "env", "e", "", "Environment ID or name (default: current)")
	return cmd
}

// resolvePortEnvID resolves the environment ID from --env flag or current target.
func resolvePortEnvID(cmd *cobra.Command, client *platform.Client, orgID, envRef string) (string, error) {
	if envRef != "" {
		return resolveEnvID(client, orgID, envRef)
	}
	// Fall back to current target.
	ref := currentEnvironmentTargetRef()
	if ref == "" {
		return "", fmt.Errorf("no environment specified and no current target set (use --env or `cw use`)")
	}
	return resolveEnvID(client, orgID, ref)
}
