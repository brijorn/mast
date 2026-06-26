package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const PeersFileName = "peers.json"

type PeerStore struct {
	Peers []string `json:"peers"`
}

func peersPath(configPath string) (string, error) {
	configPath, err := resolvePath(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(configPath), PeersFileName), nil
}

func LoadPeerStore(configPath string) (*PeerStore, error) {
	path, err := peersPath(configPath)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &PeerStore{}, nil
		}
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	var store PeerStore
	if err := json.NewDecoder(f).Decode(&store); err != nil {
		return nil, err
	}
	return &store, nil
}

func SavePeerStore(configPath string, store *PeerStore) error {
	path, err := peersPath(configPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	encoded, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0600)
}

func addSavedPeer(store *PeerStore, target string) bool {
	for _, savedPeer := range store.Peers {
		if savedPeer == target {
			return false
		}
	}
	store.Peers = append(store.Peers, target)
	return true
}
