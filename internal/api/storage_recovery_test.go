package api

import (
	"sync/atomic"
	"testing"

	"github.com/antgpu/connector/internal/hw"
)

func TestStorageRecoveryRefreshesOnlyOnHealthyTransitions(t *testing.T) {
	s := APIService{StorageReportState: &atomic.Int32{}}
	healthy := &hw.DockerStorageInfo{TotalBytes: 100, FreeBytes: 80}
	failed := &hw.DockerStorageInfo{Error: "probe pending"}
	full := &hw.DockerStorageInfo{TotalBytes: 100, FreeBytes: 0}
	for i, tc := range []struct {
		storage *hw.DockerStorageInfo
		want    bool
	}{
		{nil, false}, {failed, false}, {healthy, true}, {healthy, false},
		{failed, false}, {healthy, true}, {healthy, false},
		{&hw.DockerStorageInfo{}, false}, {healthy, true},
		{full, true}, {full, false}, {healthy, true},
	} {
		if got := s.storageNeedsSpecsRefresh(tc.storage); got != tc.want {
			t.Errorf("transition %d: got %t want %t", i, got, tc.want)
		}
	}
	if (APIService{}).storageNeedsSpecsRefresh(healthy) {
		t.Fatal("changed platforms without recovery tracking")
	}
}
