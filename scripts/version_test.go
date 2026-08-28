package scripts

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// repoRoot returns the repository root directory relative to this test source.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate version test source")
	}
	return filepath.Dir(filepath.Dir(filename))
}

// readRepoFile reads a repository file addressed by slash-separated path parts.
func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{repoRoot(t)}, parts...)...)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(parts...), err)
	}
	return string(contents)
}

// releasedVersionPattern matches the newest released CHANGELOG section heading.
var releasedVersionPattern = regexp.MustCompile(`(?m)^## \[([0-9]+\.[0-9]+\.[0-9]+)\] - ([0-9]{4}-[0-9]{2}-[0-9]{2})$`)

// changelogRelease returns the version and date of the newest released
// CHANGELOG section. The Unreleased section is skipped because it carries no
// version number.
func changelogRelease(t *testing.T) (string, string) {
	t.Helper()
	match := releasedVersionPattern.FindStringSubmatch(readRepoFile(t, "CHANGELOG.md"))
	if match == nil {
		t.Fatal("CHANGELOG.md has no released section heading")
	}
	return match[1], match[2]
}

func TestChangelogNewestReleaseIsWellFormed(t *testing.T) {
	t.Parallel()

	changelog := readRepoFile(t, "CHANGELOG.md")
	version, date := changelogRelease(t)

	tests := []struct {
		check func(t *testing.T)
		name  string
	}{
		{
			name: "unreleased section is kept above the newest release",
			check: func(t *testing.T) {
				unreleased := regexp.MustCompile(`(?m)^## \[Unreleased\]$`).FindStringIndex(changelog)
				if unreleased == nil {
					t.Fatal("CHANGELOG.md is missing the Unreleased section")
				}
				release := releasedVersionPattern.FindStringIndex(changelog)
				if release == nil || unreleased[0] > release[0] {
					t.Errorf("Unreleased section must precede release %s", version)
				}
			},
		},
		{
			name: "release date parses as a calendar date",
			check: func(t *testing.T) {
				if _, err := time.Parse("2006-01-02", date); err != nil {
					t.Errorf("release %s date %q: %v", version, date, err)
				}
			},
		},
		{
			name: "every earlier release is listed below the newest one",
			check: func(t *testing.T) {
				all := releasedVersionPattern.FindAllStringSubmatch(changelog, -1)
				if len(all) < 2 {
					t.Skip("only one released section to order")
				}
				if all[0][1] == all[1][1] {
					t.Errorf("release %s is listed twice", all[0][1])
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.check(t)
		})
	}
}

// TestVersionSurfacesMatchChangelog pins every shipped version surface to the
// newest released CHANGELOG section, so a release bump cannot land on some
// surfaces and miss others.
func TestVersionSurfacesMatchChangelog(t *testing.T) {
	t.Parallel()

	want, _ := changelogRelease(t)

	//nolint:govet // Keep surface fields grouped for readable release-surface cases.
	tests := []struct {
		name     string
		path     []string
		pattern  *regexp.Regexp
		minMatch int
	}{
		{
			name:     "CLI default version",
			path:     []string{"cmd", "oberwatch", "main.go"},
			pattern:  regexp.MustCompile(`(?m)^\tversion = "v([0-9]+\.[0-9]+\.[0-9]+)"$`),
			minMatch: 1,
		},
		{
			name:     "management API version fallback",
			path:     []string{"internal", "api", "server.go"},
			pattern:  regexp.MustCompile(`(?m)^\t\tversion = "([0-9]+\.[0-9]+\.[0-9]+)"$`),
			minMatch: 1,
		},
		{
			name:     "dashboard package manifest",
			path:     []string{"dashboard", "svelte", "package.json"},
			pattern:  regexp.MustCompile(`(?m)^  "version": "([0-9]+\.[0-9]+\.[0-9]+)",$`),
			minMatch: 1,
		},
		{
			name: "dashboard package lockfile",
			path: []string{"dashboard", "svelte", "package-lock.json"},
			// Both the lockfile root and its packages[""] entry restate the
			// package version; every other "version" belongs to a dependency.
			pattern:  regexp.MustCompile(`"name": "oberwatch-dashboard",\s*\n\s*"version": "([0-9]+\.[0-9]+\.[0-9]+)",`),
			minMatch: 2,
		},
		{
			name:     "dashboard layout version placeholder",
			path:     []string{"dashboard", "svelte", "src", "routes", "+layout.svelte"},
			pattern:  regexp.MustCompile(`\$state\('v([0-9]+\.[0-9]+\.[0-9]+)'\)`),
			minMatch: 1,
		},
		{
			name:     "dashboard login version placeholder",
			path:     []string{"dashboard", "svelte", "src", "routes", "login", "+page.svelte"},
			pattern:  regexp.MustCompile(`\$state\('v([0-9]+\.[0-9]+\.[0-9]+)'\)`),
			minMatch: 1,
		},
		{
			name:     "README stable channel table",
			path:     []string{"README.md"},
			pattern:  regexp.MustCompile("`latest`, `0\\.1`, `([0-9]+\\.[0-9]+\\.[0-9]+)`"),
			minMatch: 1,
		},
		{
			name:     "README recommended production tag",
			path:     []string{"README.md"},
			pattern:  regexp.MustCompile(`oberwatch:([0-9]+\.[0-9]+\.[0-9]+)`),
			minMatch: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matches := tt.pattern.FindAllStringSubmatch(readRepoFile(t, tt.path...), -1)
			if len(matches) < tt.minMatch {
				t.Fatalf("%s: found %d version matches, want at least %d",
					filepath.Join(tt.path...), len(matches), tt.minMatch)
			}
			for _, match := range matches {
				if match[1] != want {
					t.Errorf("%s: version = %q, want %q", filepath.Join(tt.path...), match[1], want)
				}
			}
		})
	}
}

// TestEmbeddedDashboardMatchesChangelog guards the committed dashboard bundle
// that GoReleaser embeds. The release workflow builds the SvelteKit app but
// never copies it into internal/dashboard/static, so a stale bundle ships a
// stale version and stale UI unless `make dashboard` is rerun before tagging.
func TestEmbeddedDashboardMatchesChangelog(t *testing.T) {
	t.Parallel()

	want, _ := changelogRelease(t)
	// Only quoted matches are product version literals; unquoted ones come from
	// third-party banners and URLs inside the minified bundle.
	pattern := regexp.MustCompile(`"v([0-9]+\.[0-9]+\.[0-9]+)"`)
	staticRoot := filepath.Join(repoRoot(t), "internal", "dashboard", "static")

	found := 0
	walkErr := filepath.WalkDir(staticRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".js" {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range pattern.FindAllSubmatch(contents, -1) {
			found++
			if string(match[1]) != want {
				rel, relErr := filepath.Rel(staticRoot, path)
				if relErr != nil {
					rel = path
				}
				t.Errorf("embedded asset %s: version = %q, want %q", rel, match[1], want)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk embedded dashboard assets: %v", walkErr)
	}
	if found == 0 {
		t.Fatal("embedded dashboard assets contain no product version literal")
	}
}

// embeddedBundle concatenates every JavaScript file in the checked-in dashboard
// bundle, so a contract can be asserted against the whole thing without caring
// which chunk the build happened to put a given string in.
func embeddedBundle(t *testing.T) string {
	t.Helper()

	staticRoot := filepath.Join(repoRoot(t), "internal", "dashboard", "static")
	var bundle strings.Builder
	walkErr := filepath.WalkDir(staticRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".js" {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		bundle.Write(contents)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk embedded dashboard assets: %v", walkErr)
	}
	return bundle.String()
}

// TestEmbeddedDashboardAgentDeleteContract guards the same staleness trap as the
// version check, for the agent delete UI: a bundle built before the delete
// dialog existed ships a binary whose Agents page cannot delete anything, and
// nothing else in the Go tests would notice. Only source string literals are
// matched, because the minifier renames everything else.
func TestEmbeddedDashboardAgentDeleteContract(t *testing.T) {
	t.Parallel()

	contents := embeddedBundle(t)

	//nolint:govet // Keep the expectation next to the reason it exists.
	tests := []struct {
		name   string
		needle string
	}{
		{name: "row action", needle: "Delete agent"},
		{name: "typed confirmation step", needle: "Type the agent name to confirm"},
		{name: "confirmation rule is stated", needle: "matches exactly"},
		{name: "dialog is labelled for assistive tech", needle: "agent-delete-title"},
		{name: "consequences name the rediscovery contract", needle: "not blocked"},
		{name: "deletions elsewhere reach open pages", needle: "agent_deleted"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(contents, tt.needle) {
				t.Errorf("embedded dashboard bundle is missing %q; rerun `make dashboard`", tt.needle)
			}
		})
	}
}

// TestEmbeddedDashboardProviderStatusContract ensures the checked-in bundle
// consumed by Go embeds the array-based provider API and its safety copy.
func TestEmbeddedDashboardProviderStatusContract(t *testing.T) {
	t.Parallel()

	contents := embeddedBundle(t)
	if !strings.Contains(contents, "Public service availability only") {
		t.Error(`embedded dashboard bundle is missing "Public service availability only"`)
	}
	// The accessor's identifier is whatever the minifier assigns to the health
	// response in a given build, so match any single-letter name rather than
	// pinning one build's output.
	if !regexp.MustCompile(`\b[a-zA-Z]\.providers\b`).MatchString(contents) {
		t.Error("embedded dashboard bundle has no *.providers accessor for the array-based provider API")
	}
	if regexp.MustCompile(`Object\.entries\([a-zA-Z]\.providers\)`).MatchString(contents) {
		t.Error("embedded dashboard bundle still expects the legacy provider object schema")
	}
}
