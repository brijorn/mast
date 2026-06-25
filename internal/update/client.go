package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const (
	defaultBaseURL = "https://api.github.com"
	defaultOwner   = "brijorn"
	defaultRepo    = "mast"
)

type Client struct {
	HTTPClient *http.Client
	BaseURL    string
	Owner      string
	Repo       string
}

func (c *Client) LatestRelease(ctx context.Context) (*GitHubRelease, error) {
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}

	owner := c.Owner
	if owner == "" {
		owner = defaultOwner
	}
	repo := c.Repo
	if repo == "" {
		repo = defaultRepo
	}

	base.Path = fmt.Sprintf("/repos/%s/%s/releases/latest", owner, repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "mast")

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(res.Body)

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github latest release: %s", res.Status)
	}

	var release GitHubRelease

	if err := json.NewDecoder(res.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}
