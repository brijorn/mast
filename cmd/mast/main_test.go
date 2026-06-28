package main

import (
	"reflect"
	"testing"
)

func TestTrimAndroidExecutableArg(t *testing.T) {
	executable := "/data/data/com.termux/files/usr/bin/mast"
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "leading executable",
			args: []string{executable, "version"},
			want: []string{"version"},
		},
		{
			name: "trailing executable",
			args: []string{"version", executable},
			want: []string{"version"},
		},
		{
			name: "normal command",
			args: []string{"config", "show"},
			want: []string{"config", "show"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := trimAndroidExecutableArg(test.args, executable)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("trimAndroidExecutableArg() = %#v, want %#v", got, test.want)
			}
		})
	}
}
