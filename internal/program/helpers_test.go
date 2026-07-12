package program

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/brijorn/mast/internal/node"
	"github.com/google/go-cmp/cmp"
)

func TestWriteJSONConcurrentWritesRemainValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	var wg sync.WaitGroup
	for index := 0; index < 20; index++ {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			if err := writeJSON(path, map[string]int{"value": value}); err != nil {
				t.Errorf("writeJSON: %v", err)
			}
		}(index)
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]int
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("final JSON is invalid: %v; data=%q", err, data)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".run.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestWriteRunJSONRejectsOlderRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	newer := &Run{SchemaVersion: runSchemaVersion, Revision: 2, ID: "run-1", Status: RunStatusRunning}
	older := &Run{SchemaVersion: runSchemaVersion, Revision: 1, ID: "run-1", Status: RunStatusStarting}
	if err := writeRunJSON(path, newer); err != nil {
		t.Fatal(err)
	}
	if err := writeRunJSON(path, older); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Run
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != 2 || persisted.Status != RunStatusRunning {
		t.Fatalf("persisted run = %+v, want revision 2 running", persisted)
	}
}

func TestApplyConfigReplacements(t *testing.T) {
	tmp, err := os.CreateTemp("", "test-config-replace-*.py")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())

	content := `LICENSE = "{{license_key}}"`
	if err := os.WriteFile(tmp.Name(), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	mappings := []ConfigMapping{
		{Value: "{{license_key}}"},
	}
	variables := map[string]string{
		"license_key": "my-license-123",
	}
	device := node.DeviceInfo{Serial: "device-123"}

	err = applyConfigReplacements(tmp.Name(), mappings, variables, device)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	want := `LICENSE = "my-license-123"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveValue(t *testing.T) {
	device := node.DeviceInfo{
		Serial: "RZCYA1HFRDA",
		NodeID: "node-123",
	}

	tests := []struct {
		name      string
		value     string
		variables map[string]string
		want      string
	}{
		{
			name:  "Exact built-in serial",
			value: "{{phone.serial}}",
			want:  "RZCYA1HFRDA",
		},
		{
			name:  "Exact built-in node ID",
			value: "{{phone.node_id}}",
			want:  "node-123",
		},
		{
			name:  "Unsupported built-in device serial stays unresolved",
			value: "{{device.serial}}",
			want:  "{{device.serial}}",
		},
		{
			name:  "Spaces and uppercase stays unresolved",
			value: "{{  Phone.Serial  }}",
			want:  "{{  Phone.Serial  }}",
		},
		{
			name:  "Inline built-in",
			value: "my-device-{{phone.serial}}",
			want:  "my-device-RZCYA1HFRDA",
		},
		{
			name:      "Custom variable",
			value:     "{{license}}",
			variables: map[string]string{"license": "LIC-ABC"},
			want:      "LIC-ABC",
		},
		{
			name:      "Nested variables",
			value:     "{{device_id}}",
			variables: map[string]string{"device_id": "{{phone.serial}}"},
			want:      "RZCYA1HFRDA",
		},
		{
			name:      "Unresolved variable stays",
			value:     "{{unknown}}",
			variables: map[string]string{},
			want:      "{{unknown}}",
		},
		{
			name:      "Mixed resolved and unresolved",
			value:     "{{phone.serial}}-{{unknown}}",
			variables: map[string]string{},
			want:      "RZCYA1HFRDA-{{unknown}}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveValue(tt.value, tt.variables, device)
			if got != tt.want {
				t.Errorf("resolveValue(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestADBEnvOnlyAppliesToAndroidDevices(t *testing.T) {
	nodes := []node.NodeInfo{
		{ID: "mac", Local: false, Addr: "100.103.16.24", ADBPort: 5037},
	}

	if got := adbEnv(node.DeviceInfo{
		Serial:   "ios-1",
		Platform: node.PlatformIOS,
		NodeID:   "mac",
	}, nodes); len(got) != 0 {
		t.Fatalf("iOS adb env = %+v, want empty", got)
	}

	got := adbEnv(node.DeviceInfo{
		Serial:   "android-1",
		Platform: node.PlatformAndroid,
		NodeID:   "mac",
	}, nodes)
	want := map[string]string{
		"ANDROID_SERIAL":             "android-1",
		"ADB_SERVER_SOCKET":          "tcp:100.103.16.24:5037",
		"ANDROID_ADB_SERVER_ADDRESS": "100.103.16.24",
		"ANDROID_ADB_SERVER_HOST":    "100.103.16.24",
		"ANDROID_ADB_SERVER_PORT":    "5037",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("Android adb env mismatch (-want +got):\n%s", diff)
	}
}
