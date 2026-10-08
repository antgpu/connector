package main

import (
	"reflect"
	"testing"
)

func TestReplacementLaunchArgs_DefaultForeground(t *testing.T) {
	got := replacementLaunchArgs(nil)
	want := []string{"--foreground"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replacementLaunchArgs(nil)=%v want %v", got, want)
	}
}

func TestReplacementLaunchArgs_PreservesRuntimeArgs(t *testing.T) {
	in := []string{"--foreground", "--state-dir", `C:\ProgramData\Antgpu\Connect`, "token-1"}
	got := replacementLaunchArgs(in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("replacementLaunchArgs(%v)=%v want same args", in, got)
	}
	if len(got) == 0 || len(in) == 0 {
		t.Fatal("expected non-empty args")
	}
	got[0] = "changed"
	if in[0] != "--foreground" {
		t.Fatal("replacementLaunchArgs mutated input slice")
	}
}
