package stream

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestOptionsWithDefaultsDoesNotOwnDisplayPower(t *testing.T) {
	got := Options{}.WithDefaults()
	if got.TurnScreenOff {
		t.Fatal("TurnScreenOff = true, want node power policy to own display power")
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
