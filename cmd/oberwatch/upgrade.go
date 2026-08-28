package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/OberWatch/oberwatch/internal/upgrade"
	"github.com/spf13/cobra"
)

// upgradeApplier is the privileged apply step. upgrade.Applier satisfies it;
// tests substitute a fake so no test needs root or a real install path.
type upgradeApplier interface {
	Apply(ctx context.Context) (upgrade.Result, error)
}

// newUpgradeApplier is the production factory, replaced in tests.
func newUpgradeApplier(installed upgrade.Version) upgradeApplier {
	return upgrade.NewApplier(installed)
}

func newUpgradeCmd(rootOpts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Apply an upgrade requested from the dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			printed, err := maybePrintVersion(cmd.OutOrStdout(), rootOpts.showVersion)
			if printed || err != nil {
				return err
			}
			return cmd.Help()
		},
	}

	cmd.AddCommand(newUpgradeApplyCmd(rootOpts))

	return cmd
}

func newUpgradeApplyCmd(rootOpts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "apply",
		Short: "Install the upgrade the dashboard staged, then restart the service",
		Long: "Install the upgrade the dashboard staged, then restart the service.\n\n" +
			"This is the privileged half of the in-dashboard upgrade. It is started by the\n" +
			"oberwatch-upgrade.path unit when the dashboard writes an upgrade request, and it\n" +
			"does nothing when no request is waiting.\n\n" +
			"It takes no target. The version to install is read from the request file, is\n" +
			"refused unless it is a stable release strictly newer than the installed one, and\n" +
			"is re-verified against the checksums published with that release before anything\n" +
			"is replaced. The binary being replaced is kept next to the install path for\n" +
			"rollback. Configuration and data are not touched.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			printed, err := maybePrintVersion(cmd.OutOrStdout(), rootOpts.showVersion)
			if printed || err != nil {
				return err
			}
			return runUpgradeApply(cmd.Context(), cmd.OutOrStdout(), os.Geteuid(), displayVersion(), newUpgradeApplier)
		},
	}
}

// runUpgradeApply is the body of "oberwatch upgrade apply", with the privilege
// check, the installed version and the applier passed in so it can be tested
// without root.
func runUpgradeApply(
	ctx context.Context,
	out io.Writer,
	euid int,
	installedVersion string,
	newApplier func(upgrade.Version) upgradeApplier,
) error {
	if euid != 0 {
		return fmt.Errorf("upgrade apply must run as root to replace the installed binary; it is started by the %s unit", upgrade.ApplyUnitPath)
	}

	installed, err := upgrade.ParseReleaseTag(installedVersion)
	if err != nil {
		return fmt.Errorf("the installed version %q is not a release tag, so an upgrade cannot be applied: %w", installedVersion, err)
	}

	result, err := newApplier(installed).Apply(ctx)
	if errors.Is(err, upgrade.ErrNoRequest) {
		_, writeErr := fmt.Fprintf(out, "No upgrade request is waiting in %s; nothing to do.\n", upgrade.StateDir)
		return writeErr
	}
	if err != nil {
		if result.Status != "" {
			_, _ = fmt.Fprintf(out, "%s: %s\n", result.Status, result.Message)
		}
		return err
	}

	_, writeErr := fmt.Fprintf(out, "%s: %s\n", result.Status, result.Message)
	return writeErr
}
