package update

import "context"

type UpdateChecker interface {
	Check(ctx context.Context) (*CheckResult, error)
}

type UpdateApplier interface {
	Apply(ctx context.Context, opts ApplyOptions) (*ApplyResult, error)
}

type CheckResult struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	OS              string `json:"os"`
	Arch            string `json:"arch,omitempty"`
	AssetName       string `json:"asset_name,omitempty"`
	AssetURL        string `json:"asset_url,omitempty"`
	ChecksumURL     string `json:"checksum_url,omitempty"`
}

type GitHubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []GitHubAsset `json:"assets"`
}

type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}
