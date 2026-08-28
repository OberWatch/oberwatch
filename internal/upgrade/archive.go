package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// MaxBinaryBytes bounds the extracted binary.
	MaxBinaryBytes = 256 << 20

	// maxArchiveEntries bounds how many members an archive may have.
	maxArchiveEntries = 256

	// maxDecompressedBytes bounds everything read out of one archive, including
	// the members that are skipped, so a compression bomb cannot be unpacked.
	maxDecompressedBytes = 512 << 20
)

var (
	// ErrBinaryNotInArchive means the archive did not contain the binary.
	ErrBinaryNotInArchive = errors.New("release archive does not contain the oberwatch binary")

	// ErrUnsafeArchive means the archive contained an entry that a release
	// archive never contains, such as an absolute or traversing path.
	ErrUnsafeArchive = errors.New("release archive contains an unsafe entry")
)

// ExtractBinary writes the "oberwatch" member of an already-verified release
// archive to destinationPath with mode 0755, and refuses everything else.
//
// It reads from an open handle rather than a path on purpose. The caller
// verifies the bytes behind that handle against the release checksums; taking a
// path here instead would mean re-opening the file, and the unprivileged service
// that owns the handoff directory could swap it in between.
//
// No name from the archive is ever used to open a file: the single member that
// is extracted is matched by exact name and written to the caller's path. Entry
// names that a release archive never carries — absolute paths, traversing
// paths, links, devices — are treated as a reason to stop rather than something
// to skip, because a verified release has no reason to contain them.
func ExtractBinary(archive io.Reader, destinationPath string) error {
	gzipReader, err := gzip.NewReader(io.LimitReader(archive, MaxArchiveBytes))
	if err != nil {
		return fmt.Errorf("read release archive as gzip: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()

	tarReader := tar.NewReader(io.LimitReader(gzipReader, maxDecompressedBytes))
	for entries := 0; ; entries++ {
		if entries >= maxArchiveEntries {
			return fmt.Errorf("%w: more than %d entries", ErrUnsafeArchive, maxArchiveEntries)
		}

		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w", ErrBinaryNotInArchive)
		}
		if err != nil {
			return fmt.Errorf("read release archive: %w", err)
		}
		if err := checkArchiveEntry(header); err != nil {
			return err
		}
		if header.Name != BinaryName {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("%w: %s is not a regular file", ErrUnsafeArchive, header.Name)
		}
		if header.Size > MaxBinaryBytes {
			return fmt.Errorf("%w: %s is %d bytes", ErrArtifactTooLarge, header.Name, header.Size)
		}
		return writeBinary(tarReader, destinationPath)
	}
}

// checkArchiveEntry refuses entry names and types that a release archive built
// by the project's release pipeline never produces.
func checkArchiveEntry(header *tar.Header) error {
	name := header.Name
	switch {
	case name == "":
		return fmt.Errorf("%w: empty entry name", ErrUnsafeArchive)
	case strings.HasPrefix(name, "/"), strings.HasPrefix(name, `\`):
		return fmt.Errorf("%w: absolute entry name %q", ErrUnsafeArchive, name)
	case name == "..", strings.HasPrefix(name, "../"), strings.Contains(name, "/../"), strings.HasSuffix(name, "/.."):
		return fmt.Errorf("%w: traversing entry name %q", ErrUnsafeArchive, name)
	case strings.Contains(name, "\x00"):
		return fmt.Errorf("%w: entry name %q contains a null byte", ErrUnsafeArchive, name)
	}

	switch header.Typeflag {
	case tar.TypeReg, tar.TypeDir, tar.TypeXGlobalHeader, tar.TypeXHeader:
		return nil
	default:
		return fmt.Errorf("%w: entry %q has type %q", ErrUnsafeArchive, name, string(header.Typeflag))
	}
}

// writeBinary copies the archive member into destinationPath as an executable
// file, bounded to MaxBinaryBytes. A partial or refused write leaves nothing
// behind, so a caller never finds a half-extracted binary to swap in.
func writeBinary(source io.Reader, destinationPath string) (err error) {
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, 0o755)
	if err != nil {
		return fmt.Errorf("create %s: %w", destinationPath, err)
	}
	defer func() {
		_ = destination.Close()
		if err != nil {
			_ = os.Remove(destinationPath)
		}
	}()

	written, err := io.Copy(destination, io.LimitReader(source, MaxBinaryBytes+1))
	if err != nil {
		return fmt.Errorf("write %s: %w", destinationPath, err)
	}
	if written > MaxBinaryBytes {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrArtifactTooLarge, BinaryName, int64(MaxBinaryBytes))
	}
	if written == 0 {
		return fmt.Errorf("%w: %s is empty", ErrBinaryNotInArchive, BinaryName)
	}

	// The mode is set explicitly because the process umask can clear bits that
	// OpenFile was asked for, and the swapped-in file has to stay executable.
	if err = destination.Chmod(0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", destinationPath, err)
	}
	if err = destination.Sync(); err != nil {
		return fmt.Errorf("flush %s: %w", destinationPath, err)
	}
	return nil
}
