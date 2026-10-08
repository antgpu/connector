package docker

import (
	"reflect"
	"testing"
)

func TestRunningManagedContainerFilters(t *testing.T) {
	got := runningManagedContainerFilters()
	want := map[string]string{"status": "running"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected filters: got %#v want %#v", got, want)
	}
}

func TestShellCandidatesOrder(t *testing.T) {
	candidates := shellCandidates()
	got := make([][]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, candidate.execCmd())
	}

	want := [][]string{
		{"/bin/bash", "-l"},
		{"/bin/sh", "-l"},
		{"/bin/ash", "-l"},
		{"/usr/bin/bash", "-l"},
		{"/usr/bin/sh", "-l"},
		{"/usr/bin/ash", "-l"},
		{"/bin/zsh", "-l"},
		{"/usr/bin/zsh", "-l"},
		{"/bin/fish", "-l"},
		{"/usr/bin/fish", "-l"},
		{"/bin/bash"},
		{"/bin/sh"},
		{"/bin/ash"},
		{"/usr/bin/bash"},
		{"/usr/bin/sh"},
		{"/usr/bin/ash"},
		{"/bin/zsh"},
		{"/usr/bin/zsh"},
		{"/bin/fish"},
		{"/usr/bin/fish"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected shell candidates\n got: %#v\nwant: %#v", got, want)
	}
}

func TestShellCommandProbeCmd(t *testing.T) {
	loginProbe := shellCommand{Path: "/bin/ash", Login: true}.probeCmd()
	if !reflect.DeepEqual(loginProbe, []string{"/bin/ash", "-l", "-c", "exit 0"}) {
		t.Fatalf("unexpected login probe: %#v", loginProbe)
	}

	plainProbe := shellCommand{Path: "/bin/ash"}.probeCmd()
	if !reflect.DeepEqual(plainProbe, []string{"/bin/ash", "-c", "exit 0"}) {
		t.Fatalf("unexpected plain probe: %#v", plainProbe)
	}
}
