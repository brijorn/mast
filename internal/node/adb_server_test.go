package node

import (
	"context"
	"errors"
	"strings"
	"testing"

	mastconfig "github.com/brijorn/mast/internal/config"
)

func TestEnsureADBServerSkipsWhenAndroidDisabled(t *testing.T) {
	node, err := NewNode("node-a", ":0", "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node.Close() }()

	calls := captureADBServerCommands(t, func(args []string) error {
		return nil
	})

	if err := node.EnsureADBServerForConfig(context.Background(), mastconfig.Config{AndroidEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("adb calls = %+v, want none", *calls)
	}
}

func TestEnsureADBServerLeavesReachableServerAlone(t *testing.T) {
	node, err := NewNode("node-a", ":0", "", true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node.Close() }()

	calls := captureADBServerCommands(t, func(args []string) error {
		return nil
	})

	err = node.EnsureADBServerForConfig(context.Background(), mastconfig.Config{
		AndroidEnabled: true,
		AdvertiseHost:  "10.0.0.4",
		ADBPort:        5038,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"-H 10.0.0.4 -P 5038 devices"}
	if got := *calls; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("adb calls = %+v, want %+v", got, want)
	}
}

func TestEnsureADBServerStartsPublicServerWhenAdvertisedEndpointIsUnavailable(t *testing.T) {
	node, err := NewNode("node-a", ":0", "", true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node.Close() }()

	checks := 0
	calls := captureADBServerCommands(t, func(args []string) error {
		if strings.Join(args, " ") == "-H 10.0.0.4 -P 5038 devices" {
			checks += 1
			if checks == 1 {
				return errors.New("connection refused")
			}
		}
		return nil
	})

	err = node.EnsureADBServerForConfig(context.Background(), mastconfig.Config{
		AndroidEnabled: true,
		AdvertiseHost:  "10.0.0.4",
		ADBPort:        5038,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"-H 10.0.0.4 -P 5038 devices",
		"-P 5038 kill-server",
		"-a -P 5038 start-server",
		"-H 10.0.0.4 -P 5038 devices",
	}
	if got := *calls; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("adb calls = %+v, want %+v", got, want)
	}
}

func captureADBServerCommands(t *testing.T, respond func(args []string) error) *[]string {
	t.Helper()
	original := runADBServerCommand
	var calls []string
	runADBServerCommand = func(_ context.Context, args ...string) ([]byte, error) {
		copied := append([]string(nil), args...)
		calls = append(calls, strings.Join(copied, " "))
		if err := respond(copied); err != nil {
			return nil, err
		}
		return []byte("List of devices attached\n"), nil
	}
	t.Cleanup(func() {
		runADBServerCommand = original
	})
	return &calls
}
