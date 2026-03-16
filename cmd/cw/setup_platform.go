package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/codewiresh/codewire/internal/platform"
)

func platformSetupCmd() *cobra.Command {
	var usePassword bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Interactive Codewire setup wizard",
		Long:  "Connect to a Codewire server, sign in, set your default organization, and review available resources.",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Welcome to Codewire!")
			fmt.Println()

			// Check existing config
			defaultURL := "https://codewire.sh"
			if existing, err := platform.LoadConfig(); err == nil {
				defaultURL = existing.ServerURL
				fmt.Println("Current configuration:")
				fmt.Printf("  Server:   %s\n", existing.ServerURL)

				// Try to fetch org/resource details
				c := platform.NewClientWithURL(existing.ServerURL)
				c.SessionToken = existing.SessionToken
				if orgs, err := c.ListOrgs(); err == nil {
					for _, org := range orgs {
						if org.ID == existing.DefaultOrg {
							fmt.Printf("  Org:      %s\n", org.Name)
							break
						}
					}
				}

				fmt.Println()
				ok, err := promptConfirm("Run setup again? [y/N]")
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
				fmt.Println()
			}

			// [1/6] Server URL
			serverURL, err := promptDefault("[1/6] Server URL", defaultURL)
			if err != nil {
				return err
			}

			client := platform.NewClientWithURL(serverURL)

			// Check connectivity
			if err := client.Healthz(); err != nil {
				return fmt.Errorf("cannot connect to %s: %w", serverURL, err)
			}
			fmt.Printf("      Connected to %s\n", serverURL)
			fmt.Println()

			// [2/6] Login
			fmt.Println("[2/6] Sign in")
			var displayName string
			if usePassword {
				name, err := loginWithPassword(client)
				if err != nil {
					return err
				}
				displayName = name
			} else {
				name, err := loginWithDevice(client)
				if err != nil {
					return err
				}
				displayName = name
			}
			fmt.Printf("      Logged in as %s\n", displayName)
			fmt.Println()

			// [3/6] Select or create organization
			selectedOrg, err := setupSelectOrg(client)
			if err != nil {
				return err
			}
			fmt.Println()

			// [4/6] Show resource inventory
			if selectedOrg.ID != "" {
				if err := setupListResources(client, &selectedOrg); err != nil {
					return err
				}
			}
			fmt.Println()

			// [5/6] Connect GitHub (optional)
			fmt.Println("[5/6] Connect GitHub (optional)")
			fmt.Println("      Connecting GitHub enables launching private repositories.")
			idx, ghErr := promptSelect("      Connect GitHub?", []string{"Yes", "Skip"})
			if ghErr == nil && idx == 0 {
				if err := setupGitHub(client); err != nil {
					fmt.Printf("      Warning: GitHub setup failed: %v\n", err)
					fmt.Println("      You can retry later with: cw github login")
				}
			}
			fmt.Println()

			// [6/6] SSH Setup
			fmt.Println("[6/6] SSH Setup")
			if writeErr := writeSSHConfig(); writeErr != nil {
				fmt.Printf("      Warning: SSH config update failed: %v\n", writeErr)
				fmt.Println("      You can retry later with: cw config-ssh")
			} else {
				fmt.Println("      Updated ~/.ssh/config via cw config-ssh")
			}
			fmt.Println()

			// Save config
			cfg := &platform.PlatformConfig{
				ServerURL:    serverURL,
				SessionToken: client.SessionToken,
				DefaultOrg:   selectedOrg.ID,
			}
			if err := platform.SaveConfig(cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			successMsg("Setup complete!")
			fmt.Println("  cw env create github.com/your/repo   # Create an environment")
			fmt.Println("  cw env list                          # List environments")
			return nil
		},
	}

	cmd.Flags().BoolVar(&usePassword, "password", false, "Use email/password login instead of browser")
	return cmd
}

// setupSelectOrg handles step 3: selecting or creating an organization.
func setupSelectOrg(client *platform.Client) (platform.OrgWithRole, error) {
	orgs, err := client.ListOrgs()
	if err != nil {
		return platform.OrgWithRole{}, fmt.Errorf("list orgs: %w", err)
	}

	if len(orgs) == 0 {
		return setupCreateOrg(client)
	}

	if len(orgs) == 1 {
		fmt.Printf("[3/6] Organization: %s (%s)\n", orgs[0].Name, orgs[0].Role)
		return orgs[0], nil
	}

	options := make([]string, len(orgs))
	for i, org := range orgs {
		options[i] = fmt.Sprintf("%s (%s, %d resources)", org.Name, org.Role, len(org.Resources))
	}
	idx, err := promptSelect("[3/6] Select organization:", options)
	if err != nil {
		return platform.OrgWithRole{}, err
	}
	fmt.Printf("      Default org: %s\n", orgs[idx].Name)
	return orgs[idx], nil
}

// setupCreateOrg prompts the user to create a new organization.
func setupCreateOrg(client *platform.Client) (platform.OrgWithRole, error) {
	fmt.Println("[3/6] Organization")
	ok, err := promptConfirm("      No organizations found. Create one? [Y/n]")
	if err != nil {
		return platform.OrgWithRole{}, err
	}
	if !ok {
		fmt.Println("      Skipped organization creation.")
		return platform.OrgWithRole{}, nil
	}

	name, err := prompt("      Name: ")
	if err != nil {
		return platform.OrgWithRole{}, err
	}
	if name == "" {
		return platform.OrgWithRole{}, fmt.Errorf("organization name is required")
	}

	defaultSlug := slugify(name)
	slug, err := promptDefault("      Slug", defaultSlug)
	if err != nil {
		return platform.OrgWithRole{}, err
	}

	org, err := client.CreateOrg(&platform.CreateOrgRequest{
		Name: name,
		Slug: slug,
	})
	if err != nil {
		return platform.OrgWithRole{}, fmt.Errorf("create org: %w", err)
	}

	fmt.Printf("      Created %q (%s)\n", org.Name, org.Slug)

	return platform.OrgWithRole{
		Organization: *org,
		Role:         "owner",
	}, nil
}

// setupListResources handles step 4: showing available resources in the selected org.
func setupListResources(client *platform.Client, org *platform.OrgWithRole) error {
	// Re-fetch org to get fresh resource list (in case we just created the org)
	freshOrg, err := client.GetOrg(org.ID)
	if err == nil {
		org.Resources = freshOrg.Resources
	}

	fmt.Printf("[4/6] Resources in %s\n", org.Name)
	if len(org.Resources) == 0 {
		fmt.Println("      No resources found.")
		fmt.Println("      Manage platform resources in the dashboard. The CLI manages sandbox environments.")
		return nil
	}

	resources := org.Resources
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Name < resources[j].Name
	})
	for _, r := range resources {
		health := r.HealthStatus
		if health == "" {
			health = "-"
		}
		fmt.Printf("      - %s (%s, %s, %s)\n", r.Name, r.Type, r.Status, health)
	}
	return nil
}
