package upgrade

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
)

const (
	// MaxArchiveBytes bounds a release archive download and the size of a
	// staged archive the applier will read.
	MaxArchiveBytes = 128 << 20

	// MaxChecksumsBytes bounds the checksums document.
	MaxChecksumsBytes = 64 << 10

	// maxChecksumLines bounds how many entries a checksums document may carry.
	maxChecksumLines = 512

	// maxAssetNameBytes bounds a file name in the checksums document.
	maxAssetNameBytes = 128
)

var (
	// ErrChecksumMismatch means a downloaded or staged artifact did not match
	// the SHA-256 the release publishes for it. Nothing is installed after it.
	ErrChecksumMismatch = errors.New("release artifact checksum mismatch")

	// ErrChecksumMissing means the release checksums document does not list the
	// artifact at all, so there is nothing to verify it against.
	ErrChecksumMissing = errors.New("release artifact is not listed in checksums")

	// ErrArtifactUnavailable means an artifact could not be downloaded.
	ErrArtifactUnavailable = errors.New("release artifact unavailable")

	// ErrArtifactTooLarge means an artifact exceeded its size bound.
	ErrArtifactTooLarge = errors.New("release artifact is too large")
)

// Fetcher downloads release artifacts from the public release host.
type Fetcher struct {
	// HTTPClient is bounded and follows only a short redirect chain that stays
	// on the release hosts. A nil client is replaced by the default one.
	HTTPClient *http.Client

	// BaseURL is the download prefix. Empty means DownloadBaseURL. It is a
	// struct field only so tests can point downloads at a local server;
	// nothing in the running system derives it from configuration or from a
	// request.
	BaseURL string
}

// NewFetcher builds a Fetcher against the public release download host.
func NewFetcher() *Fetcher {
	return &Fetcher{BaseURL: DownloadBaseURL, HTTPClient: newArtifactClient()}
}

// Checksums downloads and parses the checksums document published with a
// release.
func (f *Fetcher) Checksums(ctx context.Context, version Version) (map[string]string, error) {
	endpoint, err := ChecksumsURL(f.BaseURL, version)
	if err != nil {
		return nil, err
	}

	body, err := f.get(ctx, endpoint, MaxChecksumsBytes)
	if err != nil {
		return nil, err
	}
	sums, err := ParseChecksums(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ChecksumsName, err)
	}
	return sums, nil
}

// DownloadArchive downloads the release archive for a version onto disk and
// verifies it against the release checksums before the file is given its final
// name. It returns the path of the verified archive.
//
// A download that fails verification is deleted, so an unverified archive never
// remains under the name the privileged applier looks for.
func (f *Fetcher) DownloadArchive(ctx context.Context, version Version, platform Platform, stateDir string) (string, error) {
	archiveName, err := ArchiveName(version, platform)
	if err != nil {
		return "", err
	}
	finalPath, err := StagedArchivePath(stateDir, version, platform)
	if err != nil {
		return "", err
	}
	endpoint, err := ArchiveURL(f.BaseURL, version, platform)
	if err != nil {
		return "", err
	}

	sums, err := f.Checksums(ctx, version)
	if err != nil {
		return "", err
	}
	expected, ok := sums[archiveName]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrChecksumMissing, archiveName)
	}

	partialPath := finalPath + stagingSuffix
	digest, err := f.downloadToFile(ctx, endpoint, partialPath)
	if err != nil {
		_ = os.Remove(partialPath)
		return "", err
	}

	if err := VerifyDigest(expected, digest); err != nil {
		_ = os.Remove(partialPath)
		return "", fmt.Errorf("%s: %w", archiveName, err)
	}

	if err := os.Rename(partialPath, finalPath); err != nil {
		_ = os.Remove(partialPath)
		return "", fmt.Errorf("stage %s: %w", archiveName, err)
	}
	return finalPath, nil
}

// downloadToFile streams a URL into path and returns the hex SHA-256 of what
// was written. The response is bounded, so a hostile or broken server cannot
// fill the disk.
func (f *Fetcher) downloadToFile(ctx context.Context, endpoint string, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("%w: build request: %v", ErrArtifactUnavailable, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: download %s: %v", ErrArtifactUnavailable, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %s answered %d", ErrArtifactUnavailable, endpoint, resp.StatusCode)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("%w: create %s: %v", ErrArtifactUnavailable, path, err)
	}
	defer func() { _ = file.Close() }()

	hasher := sha256.New()
	// One byte over the bound is read on purpose: a body that reaches exactly
	// the limit is indistinguishable from a truncated one otherwise.
	written, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(resp.Body, MaxArchiveBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: read %s: %v", ErrArtifactUnavailable, endpoint, err)
	}
	if written > MaxArchiveBytes {
		return "", fmt.Errorf("%w: %s exceeds %d bytes", ErrArtifactTooLarge, endpoint, int64(MaxArchiveBytes))
	}
	if written == 0 {
		return "", fmt.Errorf("%w: %s answered an empty body", ErrArtifactUnavailable, endpoint)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("%w: flush %s: %v", ErrArtifactUnavailable, path, err)
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// get reads a bounded response body from a URL.
func (f *Fetcher) get(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrArtifactUnavailable, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrArtifactUnavailable, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s answered %d", ErrArtifactUnavailable, endpoint, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrArtifactUnavailable, endpoint, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrArtifactTooLarge, endpoint, limit)
	}
	return body, nil
}

func (f *Fetcher) httpClient() *http.Client {
	if f.HTTPClient == nil {
		return newArtifactClient()
	}
	return f.HTTPClient
}

// ParseChecksums reads a "sha256sum" style document into a map of asset name to
// lowercase hex digest.
//
// The parser is strict on purpose: it is the only thing standing between a
// malformed or hostile checksums document and an installed binary. A line that
// is not exactly a 64-character hex digest followed by a single-segment file
// name is an error, not a skipped line, because silently skipping a line is how
// an artifact ends up unverified.
func ParseChecksums(raw []byte) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("checksums document is empty")
	}

	sums := make(map[string]string)
	for index, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if len(sums) >= maxChecksumLines {
			return nil, fmt.Errorf("checksums document has more than %d entries", maxChecksumLines)
		}

		digest, name, err := parseChecksumLine(trimmed)
		if err != nil {
			return nil, fmt.Errorf("checksums line %d: %w", index+1, err)
		}
		if existing, found := sums[name]; found && existing != digest {
			return nil, fmt.Errorf("checksums document lists %s twice with different digests", name)
		}
		sums[name] = digest
	}

	if len(sums) == 0 {
		return nil, errors.New("checksums document has no entries")
	}
	return sums, nil
}

// parseChecksumLine splits one "<digest>  <name>" line.
func parseChecksumLine(line string) (string, string, error) {
	fields := strings.Fields(line)
	if len(fields) != 2 {
		return "", "", fmt.Errorf("expected \"<sha256>  <file>\", got %q", line)
	}

	digest := strings.ToLower(fields[0])
	if err := requireHexDigest(digest); err != nil {
		return "", "", err
	}

	// A checksums entry is a file name, never a path. Refusing separators and
	// relative segments here means a name from the document can never be joined
	// onto a directory and escape it, whatever a later caller does with it.
	name := strings.TrimPrefix(fields[1], "*")
	switch {
	case name == "":
		return "", "", errors.New("empty file name")
	case len(name) > maxAssetNameBytes:
		return "", "", fmt.Errorf("file name %q is too long", name)
	case strings.ContainsAny(name, `/\`):
		return "", "", fmt.Errorf("file name %q must not contain a path separator", name)
	case name == "." || name == "..":
		return "", "", fmt.Errorf("file name %q is not a file", name)
	case strings.ContainsAny(name, "\x00"):
		return "", "", fmt.Errorf("file name %q contains a null byte", name)
	}

	return digest, name, nil
}

func requireHexDigest(digest string) error {
	if len(digest) != sha256.Size*2 {
		return fmt.Errorf("digest %q is not %d hex characters", digest, sha256.Size*2)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("digest %q is not hex: %w", digest, err)
	}
	return nil
}

// VerifyDigest compares an expected and an actual hex SHA-256.
func VerifyDigest(expected string, actual string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	actual = strings.ToLower(strings.TrimSpace(actual))
	if err := requireHexDigest(expected); err != nil {
		return fmt.Errorf("%w: expected %v", ErrChecksumMismatch, err)
	}
	if err := requireHexDigest(actual); err != nil {
		return fmt.Errorf("%w: actual %v", ErrChecksumMismatch, err)
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
		return fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, expected, actual)
	}
	return nil
}

// OpenStagedArchive opens a staged release archive for verification.
//
// The file is opened without following a symlink and checked through the
// resulting handle rather than by a separate stat of the path, so nothing that
// can write the handoff directory can substitute a different file between the
// check and the read.
func OpenStagedArchive(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open staged archive %s: %w", path, err)
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect staged archive %s: %w", path, err)
	}
	switch {
	case !info.Mode().IsRegular():
		_ = file.Close()
		return nil, fmt.Errorf("staged archive %s is not a regular file (mode %s)", path, info.Mode())
	case info.Size() == 0:
		_ = file.Close()
		return nil, fmt.Errorf("staged archive %s is empty", path)
	case info.Size() > MaxArchiveBytes:
		_ = file.Close()
		return nil, fmt.Errorf("%w: staged archive %s is %d bytes", ErrArtifactTooLarge, path, info.Size())
	}

	return file, nil
}
