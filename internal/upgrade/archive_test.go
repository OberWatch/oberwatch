package upgrade

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// extractFrom opens an archive the way the applier does — through one handle —
// and extracts from it.
func extractFrom(t *testing.T, archivePath string, destinationPath string) error {
	t.Helper()

	archive, err := OpenStagedArchive(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()

	return ExtractBinary(archive, destinationPath)
}

func TestExtractBinary_ExtractsOnlyTheBinary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	archivePath := filepath.Join(root, "release.tar.gz")
	writeTarGz(t, archivePath, releaseEntries("v0.1.4"))

	destination := filepath.Join(root, "staged")
	if err := extractFrom(t, archivePath, destination); err != nil {
		t.Fatalf("ExtractBinary() error = %v", err)
	}

	body, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(body), "oberwatch v0.1.4") {
		t.Fatalf("extracted binary = %q, want the archive's oberwatch member", body)
	}

	info, err := os.Stat(destination)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("extracted binary mode = %o, want 755 so the swapped-in file stays executable", info.Mode().Perm())
	}

	// Nothing else from the archive was written next to it.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "release.tar.gz", "staged":
		default:
			t.Fatalf("ExtractBinary() also wrote %q; only the binary may be extracted", entry.Name())
		}
	}
}

func TestExtractBinary_RefusesUnsafeAndIncompleteArchives(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped by archive, then expectation.
	tests := []struct {
		name    string
		entries []tarEntry
		wantErr error
	}{
		{
			name:    "no binary member",
			entries: []tarEntry{{Name: "LICENSE", Body: "license", Typeflag: tar.TypeReg}},
			wantErr: ErrBinaryNotInArchive,
		},
		{
			name:    "empty archive",
			entries: nil,
			wantErr: ErrBinaryNotInArchive,
		},
		{
			name: "an absolute path entry",
			entries: []tarEntry{
				{Name: "/usr/local/bin/oberwatch", Body: "payload", Typeflag: tar.TypeReg},
				binaryEntry("v0.1.4"),
			},
			wantErr: ErrUnsafeArchive,
		},
		{
			name: "a traversing path entry",
			entries: []tarEntry{
				{Name: "../../etc/cron.d/backdoor", Body: "payload", Typeflag: tar.TypeReg},
				binaryEntry("v0.1.4"),
			},
			wantErr: ErrUnsafeArchive,
		},
		{
			name: "a nested traversing path entry",
			entries: []tarEntry{
				{Name: "docs/../../etc/passwd", Body: "payload", Typeflag: tar.TypeReg},
				binaryEntry("v0.1.4"),
			},
			wantErr: ErrUnsafeArchive,
		},
		{
			name: "a symlink entry",
			entries: []tarEntry{
				{Name: "link", Linkname: "/etc/shadow", Typeflag: tar.TypeSymlink},
				binaryEntry("v0.1.4"),
			},
			wantErr: ErrUnsafeArchive,
		},
		{
			name: "a hard link entry",
			entries: []tarEntry{
				{Name: "link", Linkname: "/etc/shadow", Typeflag: tar.TypeLink},
				binaryEntry("v0.1.4"),
			},
			wantErr: ErrUnsafeArchive,
		},
		{
			name: "a device entry",
			entries: []tarEntry{
				{Name: "dev", Typeflag: tar.TypeChar},
				binaryEntry("v0.1.4"),
			},
			wantErr: ErrUnsafeArchive,
		},
		{
			name: "a fifo entry",
			entries: []tarEntry{
				{Name: "pipe", Typeflag: tar.TypeFifo},
				binaryEntry("v0.1.4"),
			},
			wantErr: ErrUnsafeArchive,
		},
		{
			name:    "the binary name is a symlink",
			entries: []tarEntry{{Name: BinaryName, Linkname: "/bin/sh", Typeflag: tar.TypeSymlink}},
			wantErr: ErrUnsafeArchive,
		},
		{
			name:    "the binary member is empty",
			entries: []tarEntry{{Name: BinaryName, Body: "", Mode: 0o755, Typeflag: tar.TypeReg}},
			wantErr: ErrBinaryNotInArchive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			archivePath := filepath.Join(root, "release.tar.gz")
			writeTarGz(t, archivePath, tt.entries)

			destination := filepath.Join(root, "staged")
			err := extractFrom(t, archivePath, destination)
			if err == nil {
				t.Fatal("ExtractBinary() succeeded, want a refusal")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ExtractBinary() error = %v, want %v", err, tt.wantErr)
			}

			// A refused archive leaves nothing behind: no path built from an
			// archive entry, and no partially extracted destination for a caller
			// to swap in.
			entries, readErr := os.ReadDir(root)
			if readErr != nil {
				t.Fatalf("ReadDir() error = %v", readErr)
			}
			for _, entry := range entries {
				if entry.Name() != "release.tar.gz" {
					t.Fatalf("ExtractBinary() wrote %q from a refused archive", entry.Name())
				}
			}
			if _, statErr := os.Stat(destination); statErr == nil {
				t.Fatal("ExtractBinary() left a destination file behind after refusing the archive")
			}
		})
	}
}

func TestExtractBinary_RefusesAnArchiveThatIsNotAnArchive(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	notAnArchive := filepath.Join(root, "release.tar.gz")
	if err := os.WriteFile(notAnArchive, []byte("this is not gzip"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := extractFrom(t, notAnArchive, filepath.Join(root, "staged")); err == nil {
		t.Fatal("ExtractBinary() accepted a file that is not a gzip archive")
	}
}

func TestExtractBinary_RefusesASymlinkedArchive(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	real := filepath.Join(root, "real.tar.gz")
	writeTarGz(t, real, releaseEntries("v0.1.4"))

	link := filepath.Join(root, "link.tar.gz")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if err := extractFrom(t, link, filepath.Join(root, "staged")); err == nil {
		t.Fatal("ExtractBinary() read through a symlink; a planted link in the handoff directory must not be extracted")
	}
}

func TestExtractBinary_RefusesToOverwriteAnExistingDestination(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	archivePath := filepath.Join(root, "release.tar.gz")
	writeTarGz(t, archivePath, releaseEntries("v0.1.4"))

	destination := filepath.Join(root, "staged")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := extractFrom(t, archivePath, destination); err == nil {
		t.Fatal("ExtractBinary() overwrote an existing destination; the extraction must create a fresh file")
	}
	body, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(body) != "existing" {
		t.Fatalf("destination = %q, want it left untouched", body)
	}
}

func TestExtractBinary_BoundsTheEntryCount(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	entries := make([]tarEntry, 0, maxArchiveEntries+1)
	for index := 0; index <= maxArchiveEntries; index++ {
		entries = append(entries, tarEntry{Name: "filler-" + strings.Repeat("x", index%8) + string(rune('a'+index%26)) + string(rune('a'+(index/26)%26)), Body: "x", Typeflag: tar.TypeReg})
	}

	archivePath := filepath.Join(root, "release.tar.gz")
	writeTarGz(t, archivePath, entries)

	if err := extractFrom(t, archivePath, filepath.Join(root, "staged")); !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("ExtractBinary() error = %v, want ErrUnsafeArchive for an archive past the entry bound", err)
	}
}
