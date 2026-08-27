package scripts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// releaseSmokeContracts are the three contracts one release smoke run has to
// execute against a single binary.
var releaseSmokeContracts = []string{
	"scripts/test-cli-config.sh",
	"scripts/dev/test-runaway.sh",
	"scripts/dev/test-task-budget.sh",
}

// requireExecutable fails when a repository script is missing or has no
// executable bit, which makes a caller that runs it by path fail with
// "Permission denied" instead of running the script.
func requireExecutable(t *testing.T, relPath string) {
	t.Helper()

	info, err := os.Stat(filepath.Join(repoRoot(t), filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("stat %s: %v", relPath, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("%s is not executable (mode %v)", relPath, info.Mode().Perm())
	}
}

// TestReleaseSmokeRunsEveryContract pins the release evidence to all three
// contracts, so dropping one from the script cannot quietly shrink what a
// release run proves.
func TestReleaseSmokeRunsEveryContract(t *testing.T) {
	t.Parallel()

	smoke := readRepoFile(t, "scripts", "release-smoke.sh")
	requireExecutable(t, "scripts/release-smoke.sh")

	for _, contract := range releaseSmokeContracts {
		contract := contract
		t.Run(contract, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(smoke, contract) {
				t.Errorf("scripts/release-smoke.sh does not run %s", contract)
			}
			requireExecutable(t, contract)
		})
	}
}

// TestMakefileScriptRecipesAreExecutable covers every script the Makefile runs
// by path, not just the smoke ones.
func TestMakefileScriptRecipesAreExecutable(t *testing.T) {
	t.Parallel()

	makefile := readRepoFile(t, "Makefile")
	pattern := regexp.MustCompile(`\./(scripts/[A-Za-z0-9._/-]+\.sh)`)
	matches := pattern.FindAllStringSubmatch(makefile, -1)
	if len(matches) == 0 {
		t.Fatal("Makefile runs no repository script")
	}

	seen := map[string]bool{}
	for _, match := range matches {
		if seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		requireExecutable(t, match[1])
	}
}

// TestMakefileTargetsArePhony guards the target list: none of the targets
// produce a file named after the target, so all of them must be phony or a
// same-named file in the tree would skip the recipe.
func TestMakefileTargetsArePhony(t *testing.T) {
	t.Parallel()

	makefile := readRepoFile(t, "Makefile")
	phonyMatch := regexp.MustCompile(`(?m)^\.PHONY:(.*)$`).FindStringSubmatch(makefile)
	if phonyMatch == nil {
		t.Fatal("Makefile has no .PHONY declaration")
	}
	phony := map[string]bool{}
	for _, name := range strings.Fields(phonyMatch[1]) {
		phony[name] = true
	}

	targets := regexp.MustCompile(`(?m)^([a-zA-Z][a-zA-Z0-9_-]*):[^=]*$`).FindAllStringSubmatch(makefile, -1)
	if len(targets) == 0 {
		t.Fatal("Makefile declares no targets")
	}
	for _, target := range targets {
		if !phony[target[1]] {
			t.Errorf("Makefile target %q is missing from .PHONY", target[1])
		}
	}
}
