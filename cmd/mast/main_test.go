package main

import (
	"reflect"
	"testing"
)

func TestTrimAndroidExecutableArg(t *testing.T) {
	executable := "/data/data/com.termux/files/usr/bin/mast"
	executablePaths := []string{executable}
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
			got := trimAndroidExecutableArg(test.args, executablePaths)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("trimAndroidExecutableArg() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestTrimAndroidExecutableArgUsesInvokedPath(t *testing.T) {
	args := []string{"version", "/data/data/com.termux/files/home/mast"}
	executablePaths := []string{
		"./mast",
		"/data/data/com.termux/files/home/mast",
		"/proc/self/exe",
	}

	got := trimAndroidExecutableArg(args, executablePaths)
	want := []string{"version"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trimAndroidExecutableArg() = %#v, want %#v", got, want)
	}
}

func TestTrimAndroidExecutableArgUsesPathResolvedExecutable(t *testing.T) {
	args := []string{"version", "/data/data/com.termux/files/usr/bin/mast"}
	executablePaths := []string{
		"mast",
		"/data/data/com.termux/files/home/mast",
		"/data/data/com.termux/files/usr/bin/mast",
		"/proc/self/exe",
	}

	got := trimAndroidExecutableArg(args, executablePaths)
	want := []string{"version"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trimAndroidExecutableArg() = %#v, want %#v", got, want)
	}
}
