package program

import (
	"os"
	"testing"

	"github.com/brijorn/mast/internal/node"
)

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
