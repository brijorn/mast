package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/brijorn/mast/internal/update"
)

type UpdateCmd struct {
	Check UpdateCheckCmd `cmd:"" help:"Check for a Mast update"`
	Apply UpdateApplyCmd `cmd:"" help:"Apply a Mast update"`
}

type UpdateCheckCmd struct {
	ConfigPath string `name:"config" short:"c" type:"path" help:"Path to config file"`
	APIAddr    string `name:"api" help:"Local Mast API base URL"`
	NodeID     string `arg:"" optional:"" help:"Node ID to check; omit for the local node"`
}

type UpdateApplyCmd struct {
	ConfigPath string `name:"config" short:"c" type:"path" help:"Path to config file"`
	APIAddr    string `name:"api" help:"Local Mast API base URL"`
	Force      bool   `help:"Apply even when the latest version matches the current version"`
	Restart    bool   `help:"Restart Mast after applying the update"`
	NodeID     string `arg:"" optional:"" help:"Node ID to update; omit for the local node"`
}

func (u *UpdateCheckCmd) Run() error {
	apiBase, err := apiBaseURL(u.ConfigPath, u.APIAddr)
	if err != nil {
		return err
	}

	res, err := http.Get(apiBase + updatePath(u.NodeID))
	if err != nil {
		return err
	}
	defer func() {
		_ = res.Body.Close()
	}()

	return writeAPIResponse("check update", res)
}

func (u *UpdateApplyCmd) Run() error {
	apiBase, err := apiBaseURL(u.ConfigPath, u.APIAddr)
	if err != nil {
		return err
	}

	body, err := json.Marshal(update.ApplyOptions{Force: u.Force, Restart: u.Restart})
	if err != nil {
		return err
	}

	res, err := http.Post(apiBase+updatePath(u.NodeID), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer func() {
		_ = res.Body.Close()
	}()

	return writeAPIResponse("apply update", res)
}

func updatePath(nodeID string) string {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return "/api/update"
	}
	return "/api/nodes/" + url.PathEscape(nodeID) + "/update"
}

func writeAPIResponse(action string, res *http.Response) error {
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%s: %s: %s", action, res.Status, strings.TrimSpace(string(body)))
	}

	if _, err := os.Stdout.Write(body); err != nil {
		return err
	}
	if len(body) == 0 || body[len(body)-1] != '\n' {
		_, err = fmt.Fprintln(os.Stdout)
	}
	return err
}
