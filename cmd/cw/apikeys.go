package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/codewiresh/codewire/internal/platform"
)

func apiKeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "api-keys",
		Aliases: []string{"keys"},
		Short:   "Manage API keys",
	}
	cmd.AddCommand(
		apiKeysCreateCmd(),
		apiKeysListCmd(),
		apiKeysDeleteCmd(),
	)
	return cmd
}

func apiKeysCreateCmd() *cobra.Command {
	var expires int

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an API key",
		Long:  "Create an API key. The full key is shown once -- save it.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			orgID, client, err := getDefaultOrg()
			if err != nil {
				return err
			}

			req := &platform.CreateAPIKeyRequest{Name: args[0]}
			if expires > 0 {
				req.ExpiresInDays = &expires
			}

			resp, err := client.CreateAPIKey(orgID, req)
			if err != nil {
				return fmt.Errorf("create api key: %w", err)
			}

			fmt.Println(resp.Key)
			return nil
		},
	}
	cmd.Flags().IntVar(&expires, "expires", 0, "Expire after N days (0 = never)")
	return cmd
}

func apiKeysListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List API keys",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			orgID, client, err := getDefaultOrg()
			if err != nil {
				return err
			}

			keys, err := client.ListAPIKeys(orgID)
			if err != nil {
				return fmt.Errorf("list api keys: %w", err)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "PREFIX\tNAME\tID\tCREATED\n")
			for _, k := range keys {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", k.KeyPrefix, k.Name, k.ID, k.CreatedAt)
			}
			w.Flush()
			return nil
		},
	}
}

func apiKeysDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <key-id>",
		Short:   "Delete an API key",
		Aliases: []string{"rm"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			orgID, client, err := getDefaultOrg()
			if err != nil {
				return err
			}

			if err := client.DeleteAPIKey(orgID, args[0]); err != nil {
				return fmt.Errorf("delete api key: %w", err)
			}

			successMsg("API key deleted.")
			return nil
		},
	}
}
