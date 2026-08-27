package config

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// NewInitCmd returns the `oberwatch init` command.
func NewInitCmd() *cobra.Command {
	var (
		output string
		force  bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a commented starter config",
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunInit(cmd.OutOrStdout(), output, force)
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", DefaultInitOutput, "output path for the generated config")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file at the output path")
	return cmd
}

// NewValidateCmd returns the `oberwatch validate` command.
func NewValidateCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate an Oberwatch config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunValidate(cmd.OutOrStdout(), cmd.ErrOrStderr(), configPath)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "config file path")
	return cmd
}

// RunInit writes the starter config and prints the success message to stdout.
func RunInit(stdout io.Writer, output string, force bool) error {
	if output == "" {
		output = DefaultInitOutput
	}
	if err := WriteStarter(output, force); err != nil {
		return err
	}

	_, err := io.WriteString(stdout, InitSuccessMessage(output))
	return err
}

// RunValidate validates the config at path (or the search order when path is
// empty). Warnings go to stderr, one per line; stdout receives exactly
// ValidSuccessMessage on success. Any failure is returned as an error.
func RunValidate(stdout, stderr io.Writer, path string) error {
	report, err := ValidateFile(path)
	if err != nil {
		return err
	}

	for _, warning := range report.Warnings {
		if _, err = fmt.Fprintf(stderr, "warning: %s\n", warning); err != nil {
			return err
		}
	}

	_, err = io.WriteString(stdout, ValidSuccessMessage(report.Path))
	return err
}
