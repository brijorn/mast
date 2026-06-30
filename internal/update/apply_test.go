package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestApplySkipsDownloadWhenAlreadyCurrent(t *testing.T) {
	applier := Applier{
		Checker: &fakeApplyChecker{
			result: &CheckResult{
				CurrentVersion:  "0.1.0",
				LatestVersion:   "0.1.0",
				UpdateAvailable: false,
			},
		},
	}

	got, err := applier.Apply(context.Background(), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if got.Updated {
		t.Fatal("Updated = true, want false")
	}
	if got.Message != "already up to date" {
		t.Fatalf("Message = %q", got.Message)
	}
}

func TestApplyDownloadsVerifiesExtractsAndReplacesExecutable(t *testing.T) {
	dir := t.TempDir()
	executablePath := filepath.Join(dir, "mast")
	if runtime.GOOS == "windows" {
		executablePath += ".exe"
	}
	if err := os.WriteFile(executablePath, []byte("old binary"), 0755); err != nil {
		t.Fatalf("write current executable: %v", err)
	}

	archive := buildTarGzArchive(t, "mast", []byte("new binary"))
	sum := sha256.Sum256(archive)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			_, _ = fmt.Fprintf(w, "%s  mast_0.2.0_darwin_arm64.tar.gz\n", hex.EncodeToString(sum[:]))
		case "/mast_0.2.0_darwin_arm64.tar.gz":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	applier := Applier{
		Checker: &fakeApplyChecker{
			result: &CheckResult{
				CurrentVersion:  "0.1.0",
				LatestVersion:   "0.2.0",
				UpdateAvailable: true,
				AssetName:       "mast_0.2.0_darwin_arm64.tar.gz",
				AssetURL:        server.URL + "/mast_0.2.0_darwin_arm64.tar.gz",
				ChecksumURL:     server.URL + "/checksums.txt",
			},
		},
		HTTPClient:     server.Client(),
		ExecutablePath: executablePath,
	}

	got, err := applier.Apply(context.Background(), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if !got.Updated {
		t.Fatal("Updated = false, want true")
	}
	if !got.RestartRequired {
		t.Fatal("RestartRequired = false, want true")
	}

	replaced, err := os.ReadFile(executablePath)
	if err != nil {
		t.Fatalf("read replaced executable: %v", err)
	}
	if string(replaced) != "new binary" {
		t.Fatalf("executable = %q, want new binary", replaced)
	}
}

func TestReplaceExecutablePreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mast")
	if err := os.WriteFile(path, []byte("old binary"), 0755); err != nil {
		t.Fatalf("write current executable: %v", err)
	}
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatalf("chmod current executable: %v", err)
	}

	if err := replaceExecutable(path, []byte("new binary")); err != nil {
		t.Fatalf("replaceExecutable returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read executable: %v", err)
	}
	if string(got) != "new binary" {
		t.Fatalf("executable = %q, want new binary", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat executable: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
}

func TestReplaceExecutableRejectsEmptyBinary(t *testing.T) {
	err := replaceExecutable(filepath.Join(t.TempDir(), "mast"), nil)
	if err == nil {
		t.Fatal("replaceExecutable returned nil error, want empty binary error")
	}
	if !strings.Contains(err.Error(), "replacement binary is empty") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestVerifyChecksumAcceptsMatchingAsset(t *testing.T) {
	asset := []byte("release bytes")
	sum := sha256.Sum256(asset)
	checksums := []byte(fmt.Sprintf("%s  mast_0.2.0_darwin_arm64.tar.gz\n", hex.EncodeToString(sum[:])))

	if err := verifyChecksum("mast_0.2.0_darwin_arm64.tar.gz", asset, checksums); err != nil {
		t.Fatalf("verifyChecksum returned error: %v", err)
	}
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	want := []byte("unix binary")
	archive := buildTarGzArchive(t, "mast", want)

	got, err := extractBinary("mast_0.2.0_darwin_arm64.tar.gz", archive)
	if err != nil {
		t.Fatalf("extractBinary returned error: %v", err)
	}

	if string(got) != string(want) {
		t.Fatalf("binary = %q, want %q", got, want)
	}
}

func TestExtractBinaryFromZip(t *testing.T) {
	want := []byte("windows binary")
	archive := buildZipArchive(t, "mast.exe", want)

	got, err := extractBinary("mast_0.2.0_windows_amd64.zip", archive)
	if err != nil {
		t.Fatalf("extractBinary returned error: %v", err)
	}

	if string(got) != string(want) {
		t.Fatalf("binary = %q, want %q", got, want)
	}
}

func TestExtractBinaryRejectsMissingBinary(t *testing.T) {
	archive := buildTarGzArchive(t, "README.md", []byte("not a binary"))

	_, err := extractBinary("mast_0.2.0_linux_amd64.tar.gz", archive)
	if err == nil {
		t.Fatal("extractBinary returned nil error, want missing binary error")
	}
	if !strings.Contains(err.Error(), "mast binary not found") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestExtractBinaryRejectsUnsupportedArchive(t *testing.T) {
	_, err := extractBinary("mast_0.2.0_linux_amd64.bin", []byte("binary"))
	if err == nil {
		t.Fatal("extractBinary returned nil error, want unsupported archive error")
	}
	if !strings.Contains(err.Error(), "unsupported archive type") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestVerifyChecksumRejectsMismatch(t *testing.T) {
	err := verifyChecksum(
		"mast_0.2.0_darwin_arm64.tar.gz",
		[]byte("release bytes"),
		[]byte("0000000000000000000000000000000000000000000000000000000000000000  mast_0.2.0_darwin_arm64.tar.gz\n"),
	)

	if err == nil {
		t.Fatal("verifyChecksum returned nil error, want mismatch")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestVerifyChecksumRejectsMissingAsset(t *testing.T) {
	err := verifyChecksum(
		"mast_0.2.0_darwin_arm64.tar.gz",
		[]byte("release bytes"),
		[]byte("abcd  other-file.tar.gz\n"),
	)

	if err == nil {
		t.Fatal("verifyChecksum returned nil error, want missing checksum")
	}
	if !strings.Contains(err.Error(), "checksum for mast_0.2.0_darwin_arm64.tar.gz not found") {
		t.Fatalf("error = %q", err.Error())
	}
}

type fakeApplyChecker struct {
	result *CheckResult
	err    error
}

func (f *fakeApplyChecker) Check(_ context.Context) (*CheckResult, error) {
	return f.result, f.err
}

func buildTarGzArchive(t *testing.T, name string, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0755,
		Size: int64(len(body)),
	}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("write tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}

	return buf.Bytes()
}

func buildZipArchive(t *testing.T, name string, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("create zip file: %v", err)
	}
	if _, err := io.Copy(w, bytes.NewReader(body)); err != nil {
		t.Fatalf("write zip body: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	return buf.Bytes()
}
