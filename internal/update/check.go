package update

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/brijorn/mast/internal/version"
)

type Checker struct {
	Client         *Client
	CurrentVersion string
	OS             string
	Arch           string
}

func (c *Checker) Check(ctx context.Context) (*CheckResult, error) {
	current := c.CurrentVersion
	if current == "" {
		current = version.Version
	}
	goos := c.OS
	if goos == "" {
		goos = runtime.GOOS
	}
	arch := c.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}

	client := c.Client
	if client == nil {
		client = &Client{
			Owner: defaultOwner,
			Repo:  defaultRepo,
		}
	}

	release, err := client.LatestRelease(ctx)
	if err != nil {
		return nil, err
	}
	latest := normalizeVersion(release.TagName)
	if latest == "" {
		return nil, fmt.Errorf("latest release has empty tag")
	}

	assetName := expectedAssetName(latest, goos, arch)
	asset, ok := findAsset(release.Assets, assetName)
	if !ok {
		return nil, fmt.Errorf("release %s has no asset for %s/%s", latest, goos, arch)
	}

	checksum, ok := findAsset(release.Assets, "checksums.txt")
	if !ok {
		return nil, fmt.Errorf("release %s has no checksums.txt asset", latest)
	}

	return &CheckResult{
		CurrentVersion:  current,
		LatestVersion:   latest,
		UpdateAvailable: updateAvailable(current, latest),
		OS:              goos,
		Arch:            arch,
		AssetName:       asset.Name,
		AssetURL:        asset.BrowserDownloadURL,
		ChecksumURL:     checksum.BrowserDownloadURL,
	}, nil
}

func expectedAssetName(version string, goos string, arch string) string {
	return fmt.Sprintf("mast_%s_%s_%s%s", version, goos, arch, archiveExt(goos))
}

func archiveExt(goos string) string {
	if goos == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

func findAsset(assets []GitHubAsset, name string) (GitHubAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return GitHubAsset{}, false
}

func updateAvailable(current string, latest string) bool {
	current = normalizeVersion(current)
	if current == "" || current == "dev" || current == "unknown" {
		return false
	}
	return current != latest
}

func normalizeVersion(v string) string {
	return strings.TrimPrefix(v, "v")
}
