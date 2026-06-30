package stream

import "testing"

func TestOptionsWithDefaultsTurnsScreenOffWhenControlEnabled(t *testing.T) {
	got := Options{}.WithDefaults()
	if !got.TurnScreenOff {
		t.Fatalf("TurnScreenOff = false, want true")
	}
}

func TestOptionsWithDefaultsDoesNotTurnScreenOffWithoutControl(t *testing.T) {
	got := Options{NoControl: true}.WithDefaults()
	if got.TurnScreenOff {
		t.Fatalf("TurnScreenOff = true, want false when control is disabled")
	}
}
