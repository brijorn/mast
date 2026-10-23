package node

import "testing"

func TestParseOffModeCharge(t *testing.T) {
	cases := []struct {
		name, output, want string
	}{
		// fastboot answers getvar on stderr, then a timing line.
		{"pixel charging screen", "off-mode-charge: 1\nFinished. Total time: 0.001s\n", "1"},
		{"pixel boots on power", "off-mode-charge: 0\r\nFinished. Total time: 0.000s\r\n", "0"},
		{"bootloader without the variable", "getvar:off-mode-charge FAILED (remote: 'GetVar Variable Not found')\n", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		if got := parseOffModeCharge(tc.output); got != tc.want {
			t.Errorf("%s: parseOffModeCharge = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFastbootListsSerial(t *testing.T) {
	cases := []struct {
		name, output string
		want         bool
	}{
		{"listed in fastboot", "1A091FDF600KW6\t fastboot\n", true},
		{"another phone only", "1A121FDF6009EU\t fastboot\n", false},
		{"serial as a prefix", "1A091FDF600KW6X\t fastboot\n", false},
		{"nothing in fastboot", "", false},
	}
	for _, tc := range cases {
		if got := fastbootListsSerial(tc.output, "1A091FDF600KW6"); got != tc.want {
			t.Errorf("%s: fastbootListsSerial = %v, want %v", tc.name, got, tc.want)
		}
	}
}
