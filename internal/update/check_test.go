package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckReturnsUpdateAsset(t *testing.T) {
	server := newReleaseServer(t, `{
		"tag_name": "v0.2.0",
		"assets": [
			{
				"name": "checksums.txt",
				"browser_download_url": "https://example.com/checksums.txt"
			},
			{
				"name": "mast_0.2.0_darwin_arm64.tar.gz",
				"browser_download_url": "https://example.com/mast_0.2.0_darwin_arm64.tar.gz"
			}
		]
	}`)
	defer server.Close()

	checker := Checker{
		Client: &Client{
			HTTPClient: server.Client(),
			BaseURL:    server.URL,
			Owner:      "brijorn",
			Repo:       "mast",
		},
		CurrentVersion: "0.1.0",
		OS:             "darwin",
		Arch:           "arm64",
	}

	got, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	if !got.UpdateAvailable {
		t.Fatal("UpdateAvailable = false, want true")
	}
	if got.LatestVersion != "0.2.0" {
		t.Fatalf("LatestVersion = %q, want %q", got.LatestVersion, "0.2.0")
	}
	if got.AssetName != "mast_0.2.0_darwin_arm64.tar.gz" {
		t.Fatalf("AssetName = %q", got.AssetName)
	}
	if got.AssetURL != "https://example.com/mast_0.2.0_darwin_arm64.tar.gz" {
		t.Fatalf("AssetURL = %q", got.AssetURL)
	}
	if got.ChecksumURL != "https://example.com/checksums.txt" {
		t.Fatalf("ChecksumURL = %q", got.ChecksumURL)
	}
}

func TestCheckReturnsNoUpdateForSameVersion(t *testing.T) {
	server := newReleaseServer(t, `{
		"tag_name": "v0.1.0",
		"assets": [
			{
				"name": "checksums.txt",
				"browser_download_url": "https://example.com/checksums.txt"
			},
			{
				"name": "mast_0.1.0_linux_amd64.tar.gz",
				"browser_download_url": "https://example.com/mast_0.1.0_linux_amd64.tar.gz"
			}
		]
	}`)
	defer server.Close()

	checker := Checker{
		Client: &Client{
			HTTPClient: server.Client(),
			BaseURL:    server.URL,
		},
		CurrentVersion: "0.1.0",
		OS:             "linux",
		Arch:           "amd64",
	}

	got, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	if got.UpdateAvailable {
		t.Fatal("UpdateAvailable = true, want false")
	}
}

func TestCheckUsesZipForWindows(t *testing.T) {
	server := newReleaseServer(t, `{
		"tag_name": "v0.2.0",
		"assets": [
			{
				"name": "checksums.txt",
				"browser_download_url": "https://example.com/checksums.txt"
			},
			{
				"name": "mast_0.2.0_windows_amd64.zip",
				"browser_download_url": "https://example.com/mast_0.2.0_windows_amd64.zip"
			}
		]
	}`)
	defer server.Close()

	checker := Checker{
		Client: &Client{
			HTTPClient: server.Client(),
			BaseURL:    server.URL,
		},
		CurrentVersion: "0.1.0",
		OS:             "windows",
		Arch:           "amd64",
	}

	got, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	if got.AssetName != "mast_0.2.0_windows_amd64.zip" {
		t.Fatalf("AssetName = %q", got.AssetName)
	}
}

func TestCheckReturnsErrorForMissingPlatformAsset(t *testing.T) {
	server := newReleaseServer(t, `{
		"tag_name": "v0.2.0",
		"assets": [
			{
				"name": "checksums.txt",
				"browser_download_url": "https://example.com/checksums.txt"
			}
		]
	}`)
	defer server.Close()

	checker := Checker{
		Client: &Client{
			HTTPClient: server.Client(),
			BaseURL:    server.URL,
		},
		CurrentVersion: "0.1.0",
		OS:             "linux",
		Arch:           "amd64",
	}

	_, err := checker.Check(context.Background())
	if err == nil {
		t.Fatal("Check returned nil error, want missing asset error")
	}
	if !strings.Contains(err.Error(), "no asset for linux/amd64") {
		t.Fatalf("error = %q", err.Error())
	}
}

func newReleaseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/repos/brijorn/mast/releases/latest" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != "mast" {
			t.Fatalf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}
