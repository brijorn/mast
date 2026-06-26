package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Applier struct {
	Checker        UpdateChecker
	HTTPClient     *http.Client
	ExecutablePath string
}

type ApplyOptions struct {
	Force   bool `json:"force"`
	Restart bool `json:"restart"`
}
type ApplyResult struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	Updated         bool   `json:"updated"`
	RestartRequired bool   `json:"restart_required"`
	Message         string `json:"message"`
}

func (a *Applier) Apply(ctx context.Context, opts ApplyOptions) (*ApplyResult, error) {
	checker := a.Checker
	if checker == nil {
		checker = &Checker{}
	}

	check, err := checker.Check(ctx)
	if err != nil {
		return nil, err
	}

	if !check.UpdateAvailable && !opts.Force {
		return &ApplyResult{
			CurrentVersion:  check.CurrentVersion,
			LatestVersion:   check.LatestVersion,
			Updated:         false,
			RestartRequired: false,
			Message:         "already up to date",
		}, nil
	}

	checksums, err := download(ctx, a.HTTPClient, check.ChecksumURL)
	if err != nil {
		return nil, err
	}
	asset, err := download(ctx, a.HTTPClient, check.AssetURL)
	if err != nil {
		return nil, err
	}

	if err := verifyChecksum(check.AssetName, asset, checksums); err != nil {
		return nil, err
	}

	binary, err := extractBinary(check.AssetName, asset)
	if err != nil {
		return nil, err
	}

	if err := replaceExecutable(a.ExecutablePath, binary); err != nil {
		return nil, err
	}

	return &ApplyResult{
		CurrentVersion:  check.CurrentVersion,
		LatestVersion:   check.LatestVersion,
		Updated:         true,
		RestartRequired: true,
		Message:         fmt.Sprintf("updated to %s; restart required", check.LatestVersion),
	}, nil
}

func replaceExecutable(path string, binary []byte) error {
	if len(binary) == 0 {
		return fmt.Errorf("replacement binary is empty")
	}

	if path == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		path = executable
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(binary); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, path); err == nil {
		keepTemp = false
		return nil
	}

	backupPath := filepath.Join(dir, "."+base+".old")
	_ = os.Remove(backupPath)
	if err := os.Rename(path, backupPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Rename(backupPath, path)
		return err
	}

	keepTemp = false
	_ = os.Remove(backupPath)
	return nil
}

func extractBinary(assetName string, archive []byte) ([]byte, error) {
	switch {
	case strings.HasSuffix(assetName, ".tar.gz"):
		return extractTarGzBinary(archive)
	case strings.HasSuffix(assetName, ".zip"):
		return extractZipBinary(archive)
	default:
		return nil, fmt.Errorf("unsupported archive type: %s", assetName)
	}
}

func extractTarGzBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = gz.Close()
	}()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(header.Name) != "mast" {
			continue
		}

		return io.ReadAll(tr)
	}

	return nil, fmt.Errorf("mast binary not found in archive")
}

func extractZipBinary(archive []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}

	for _, file := range zr.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if filepath.Base(file.Name) != "mast.exe" {
			continue
		}

		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer func() {
			_ = rc.Close()
		}()

		return io.ReadAll(rc)
	}

	return nil, fmt.Errorf("mast.exe binary not found in archive")
}

func verifyChecksum(assetName string, asset []byte, checksums []byte) error {
	expected, err := checksumForAsset(assetName, checksums)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(asset)
	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s", assetName)
	}

	return nil
}

func checksumForAsset(assetName string, checksums []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(checksums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		if fields[1] == assetName {
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}

	return "", fmt.Errorf("checksum for %s not found", assetName)
}

func download(ctx context.Context, httpClient *http.Client, rawURL string) ([]byte, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("download url required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mast")
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", rawURL, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}
