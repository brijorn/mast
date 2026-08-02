package stream

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestOptionsWithDefaultsDoesNotOwnDisplayPower(t *testing.T) {
	got := Options{}.WithDefaults(Defaults{})
	if got.TurnScreenOff {
		t.Fatal("TurnScreenOff = true, want node power policy to own display power")
	}
}

func TestOptionsWithDefaultsFillsUnsetEncoderSettings(t *testing.T) {
	defaults := Defaults{MaxSize: 720, VideoBitrate: 4_000_000, VideoCodecOptions: "i-frame-interval=10"}
	got := Options{}.WithDefaults(defaults)
	if got.MaxSize != 720 || got.VideoBitrate != 4_000_000 || got.VideoCodecOptions != "i-frame-interval=10" {
		t.Fatalf("options = %+v, want the node defaults applied to every unset encoder field", got)
	}
}

// A caller that states an encoder setting is choosing it deliberately, so the
// node default must not overwrite it. This is what lets one stream be retuned
// without changing the node.
func TestOptionsWithDefaultsKeepsCallerEncoderSettings(t *testing.T) {
	defaults := Defaults{MaxSize: 720, VideoBitrate: 4_000_000, VideoCodecOptions: "i-frame-interval=10"}
	got := Options{MaxSize: 1080, VideoBitrate: 1_500_000, VideoCodecOptions: "i-frame-interval=1"}.WithDefaults(defaults)
	if got.MaxSize != 1080 || got.VideoBitrate != 1_500_000 || got.VideoCodecOptions != "i-frame-interval=1" {
		t.Fatalf("options = %+v, want the caller's own encoder settings preserved", got)
	}
}

func TestOptionsFormatCanPreventViewerPowerRestore(t *testing.T) {
	got := (&Options{
		NoAudio:        true,
		DisablePowerOn: true,
		DisableCleanup: true,
	}).Format()
	want := []string{
		"audio=false",
		"control=true",
		"stay_awake=false",
		"clipboard_autosync=false",
		"power_on=false",
		"cleanup=false",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("formatted options mismatch (-want +got):\n%s", diff)
	}
}
