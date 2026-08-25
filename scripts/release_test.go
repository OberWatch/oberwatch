package scripts

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func releaseConfigPath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate release test source")
	}
	return filepath.Join(filepath.Dir(filepath.Dir(filename)), ".goreleaser.yml")
}

func TestGoReleaserLinuxBuildContract(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile(releaseConfigPath(t))
	if err != nil {
		t.Fatalf("read GoReleaser config: %v", err)
	}

	buildPattern := regexp.MustCompile(`(?m)^  - id: ([^\n]+)$`)
	matches := buildPattern.FindAllSubmatchIndex(contents, -1)
	linuxBuilds := 0
	for index, match := range matches {
		blockEnd := len(contents)
		if index+1 < len(matches) {
			blockEnd = matches[index+1][0]
		}
		build := contents[match[0]:blockEnd]
		if !bytes.Contains(build, []byte("goos: [linux]")) {
			continue
		}
		linuxBuilds++
		buildID := string(contents[match[2]:match[3]])
		tests := []struct {
			name   string
			ldflag string
		}{
			{name: "version includes v prefix", ldflag: "-X main.version=v{{.Version}}"},
			{name: "build date populates CLI built field", ldflag: "-X main.built={{.Date}}"},
		}
		for _, tt := range tests {
			tt := tt
			t.Run(buildID+" "+tt.name, func(t *testing.T) {
				t.Parallel()
				if !bytes.Contains(build, []byte(tt.ldflag)) {
					t.Errorf("Linux build %q does not contain ldflag %q", buildID, tt.ldflag)
				}
			})
		}
	}
	if linuxBuilds == 0 {
		t.Fatal("GoReleaser config contains no Linux builds")
	}

	tests := []struct {
		name      string
		forbidden string
	}{
		{name: "Darwin targets remain unsupported", forbidden: "darwin"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if bytes.Contains(bytes.ToLower(contents), []byte(tt.forbidden)) {
				t.Errorf("GoReleaser config contains unsupported target %q", tt.forbidden)
			}
		})
	}
}
