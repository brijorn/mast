package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
)

func TestUpdateNodeConfigRoutesToPeerAndPersists(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	path := filepath.Join(t.TempDir(), "config.json")
	cfg := mastconfig.Default()
	cfg.AndroidEnabled = false
	cfg.APIAddr = ":6271"
	if err := mastconfig.Save(path, &cfg); err != nil {
		t.Fatal(err)
	}
	nodeB.SetConfig(path, cfg, nil)
	connectNodePair(t, nodeA, nodeB)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := nodeA.UpdateNodeConfig(ctx, "b", map[string]string{"android_enabled": "true", "api_addr": ":7001"})
	if err != nil {
		t.Fatalf("UpdateNodeConfig returned error: %v", err)
	}

	if !result.Config.AndroidEnabled || result.Config.APIAddr != ":7001" {
		t.Fatalf("result config = %+v", result.Config)
	}
	if !result.RestartRequired || len(result.RestartRequiredKeys) != 1 || result.RestartRequiredKeys[0] != "api_addr" {
		t.Fatalf("result = %+v", result)
	}
	if !nodeB.AndroidEnabled {
		t.Fatal("peer runtime AndroidEnabled was not updated")
	}

	persisted, err := mastconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.AndroidEnabled || persisted.APIAddr != ":7001" {
		t.Fatalf("persisted config = %+v", persisted)
	}
}

func TestGetNodeConfigRoutesToPeer(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	cfg := mastconfig.Default()
	cfg.ADBPort = 5038
	nodeB.SetConfig(filepath.Join(t.TempDir(), "config.json"), cfg, nil)
	connectNodePair(t, nodeA, nodeB)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := nodeA.GetNodeConfig(ctx, "b")
	if err != nil {
		t.Fatalf("GetNodeConfig returned error: %v", err)
	}
	if got.ADBPort != 5038 {
		t.Fatalf("config = %+v", got)
	}
}

func TestUpdateNodeConfigValidationDoesNotRewriteConfig(t *testing.T) {
	node, err := NewNode("a", ":0", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node.Close() }()

	path := filepath.Join(t.TempDir(), "config.json")
	cfg := mastconfig.Default()
	cfg.AndroidEnabled = false
	if err := mastconfig.Save(path, &cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	node.SetConfig(path, cfg, nil)

	_, err = node.UpdateNodeConfig(context.Background(), "a", map[string]string{"nope": "true", "android_enabled": "true"})
	if err == nil {
		t.Fatal("UpdateNodeConfig returned nil error for invalid key")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("config file changed after failed validation\nbefore: %s\nafter: %s", before, after)
	}
	if node.AndroidEnabled {
		t.Fatal("runtime config changed after failed validation")
	}
}
