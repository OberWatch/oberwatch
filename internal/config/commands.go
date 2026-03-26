package config

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NewInitCmd returns the `oberwatch init` command.
func NewInitCmd() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a commented starter config",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := GenerateStarter(output); err != nil {
				return err
			}

			_, err := fmt.Fprintf(cmd.OutOrStdout(), "wrote starter config to %s\n", output)
			return err
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "oberwatch.toml", "output path for the generated config")
	return cmd
}

// NewValidateCmd returns the `oberwatch validate` command.
func NewValidateCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate an Oberwatch config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveConfigPath(configPath)
			if err != nil {
				return err
			}

			_, err = Load(resolvedPath)
			if err != nil {
				return err
			}

			_, err = fmt.Fprintf(cmd.OutOrStdout(), "config %s is valid\n", resolvedPath)
			return err
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "config file path")
	return cmd
}
