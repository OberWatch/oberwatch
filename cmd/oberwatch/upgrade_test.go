package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/OberWatch/oberwatch/internal/upgrade"
)

// stubApplier records that it was called and returns a fixed outcome.
//
//nolint:govet // Keep the stub grouped by what it records.
type stubApplier struct {
	result upgrade.Result
	err    error
	calls  int
}

func (s *stubApplier) Apply(context.Context) (upgrade.Result, error) {
	s.calls++
	return s.result, s.err
}

func TestRunUpgradeApply(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped by setup, then expectation.
	tests := []struct {
		name             string
		euid             int
		installedVersion string
		applier          *stubApplier
		wantErr          bool
		wantCalls        int
		wantErrContains  string
		wantOutContains  string
	}{
		{
			name:             "an unprivileged run is refused before anything is read",
			euid:             1000,
			installedVersion: "v0.1.3",
			applier:          &stubApplier{},
			wantErr:          true,
			wantErrContains:  "must run as root",
			wantCalls:        0,
		},
		{
			name:             "a build that is not a release cannot be upgraded from",
			euid:             0,
			installedVersion: "dev",
			applier:          &stubApplier{},
			wantErr:          true,
			wantErrContains:  "not a release tag",
			wantCalls:        0,
		},
		{
			name:             "an unprefixed version cannot be upgraded from",
			euid:             0,
			installedVersion: "0.1.3",
			applier:          &stubApplier{},
			wantErr:          true,
			wantErrContains:  "not a release tag",
			wantCalls:        0,
		},
		{
			name:             "no waiting request is not a failure",
			euid:             0,
			installedVersion: "v0.1.3",
			applier:          &stubApplier{err: upgrade.ErrNoRequest},
			wantCalls:        1,
			wantOutContains:  "nothing to do",
		},
		{
			name:             "a successful apply is reported",
			euid:             0,
			installedVersion: "v0.1.3",
			applier: &stubApplier{result: upgrade.Result{
				Status:  upgrade.ResultSucceeded,
				Tag:     "v0.1.4",
				Message: "Installed v0.1.4 and restarted oberwatch.",
			}},
			wantCalls:       1,
			wantOutContains: "succeeded: Installed v0.1.4",
		},
		{
			name:             "an installed-but-not-restarted apply is reported",
			euid:             0,
			installedVersion: "v0.1.3",
			applier: &stubApplier{result: upgrade.Result{
				Status:  upgrade.ResultRestartRequired,
				Tag:     "v0.1.4",
				Message: "Installed v0.1.4 but the restart failed.",
			}},
			wantCalls:       1,
			wantOutContains: "restart_required",
		},
		{
			name:             "a refusal is a failure with the reason printed",
			euid:             0,
			installedVersion: "v0.1.3",
			applier: &stubApplier{
				result: upgrade.Result{
					Status:  upgrade.ResultFailed,
					Tag:     "v0.1.4",
					Message: "checksum mismatch. Nothing was installed.",
				},
				err: upgrade.ErrRefused,
			},
			wantErr:         true,
			wantErrContains: "refused",
			wantCalls:       1,
			wantOutContains: "checksum mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			var installed upgrade.Version
			factory := func(version upgrade.Version) upgradeApplier {
				installed = version
				return tt.applier
			}

			err := runUpgradeApply(context.Background(), &out, tt.euid, tt.installedVersion, factory)

			if tt.wantErr != (err != nil) {
				t.Fatalf("runUpgradeApply() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("runUpgradeApply() error = %v, want it to mention %q", err, tt.wantErrContains)
			}
			if tt.applier.calls != tt.wantCalls {
				t.Fatalf("applier called %d times, want %d", tt.applier.calls, tt.wantCalls)
			}
			if tt.wantOutContains != "" && !strings.Contains(out.String(), tt.wantOutContains) {
				t.Fatalf("output = %q, want it to mention %q", out.String(), tt.wantOutContains)
			}
			if tt.wantCalls > 0 && installed.Tag() != tt.installedVersion {
				t.Fatalf("applier built for %s, want the installed version %s", installed, tt.installedVersion)
			}
		})
	}
}

func TestNewUpgradeApplier_BuildsAnApplierForTheInstalledLocations(t *testing.T) {
	t.Parallel()

	version, err := upgrade.ParseReleaseTag("v0.1.3")
	if err != nil {
		t.Fatalf("ParseReleaseTag() error = %v", err)
	}

	applier, ok := newUpgradeApplier(version).(*upgrade.Applier)
	if !ok {
		t.Fatalf("newUpgradeApplier() = %T, want *upgrade.Applier", newUpgradeApplier(version))
	}
	if applier.StateDir != upgrade.StateDir {
		t.Errorf("StateDir = %q, want %q", applier.StateDir, upgrade.StateDir)
	}
	if applier.Installed != version {
		t.Errorf("Installed = %s, want %s", applier.Installed, version)
	}
}

func TestUpgradeCommand_IsRegisteredAndTakesNoTarget(t *testing.T) {
	t.Parallel()

	root := newRootCmd()

	apply, _, err := root.Find([]string{"upgrade", "apply"})
	if err != nil {
		t.Fatalf("Find(upgrade apply) error = %v", err)
	}
	if apply == nil || apply.Name() != "apply" {
		t.Fatalf("Find(upgrade apply) = %v, want the apply subcommand", apply)
	}
	if apply.Flags().HasFlags() {
		t.Error("upgrade apply defines flags; it must take no target, so there is nothing to point it somewhere else")
	}
	if err := apply.Args(apply, []string{"v9.9.9"}); err == nil {
		t.Error("upgrade apply accepted a positional argument; the version comes from the request file only")
	}

	if !strings.Contains(apply.Long, "rollback") {
		t.Error("upgrade apply help does not mention rollback")
	}
	if !strings.Contains(apply.Long, "Configuration and data are not touched") {
		t.Error("upgrade apply help does not state that configuration and data are left alone")
	}
}

func TestRunUpgradeApply_ReportsAnUnexpectedApplyFailure(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	stub := &stubApplier{err: errors.New("disk full")}
	err := runUpgradeApply(context.Background(), &out, 0, "v0.1.3", func(upgrade.Version) upgradeApplier { return stub })

	if err == nil {
		t.Fatal("runUpgradeApply() hid an unexpected failure")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("runUpgradeApply() error = %v, want the real reason", err)
	}
}
