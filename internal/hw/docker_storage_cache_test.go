package hw

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedDockerStorageUsesBothLimits(t *testing.T) {
	for _, tc := range []struct {
		name                                                            string
		hostTotal, hostFree, guestTotal, guestFree, wantTotal, wantFree uint64
	}{
		{"guest_limit", 1000, 465, 316, 298, 316, 298},
		{"host_limit", 1000, 100, 2000, 1800, 1000, 100},
		{"full_host", 1000, 0, 500, 400, 500, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := boundedDockerStorage(tc.hostTotal, tc.hostFree, tc.guestTotal, tc.guestFree)
			if err != nil || s.TotalBytes != tc.wantTotal || s.FreeBytes != tc.wantFree || s.UsedBytes != s.TotalBytes-s.FreeBytes {
				t.Fatalf("unexpected bounded capacity: %+v %v", s, err)
			}
		})
	}
	for _, values := range [][4]uint64{{0, 0, 100, 50}, {100, 101, 100, 50}, {100, 50, 0, 0}, {100, 50, 100, 101}} {
		if _, err := boundedDockerStorage(values[0], values[1], values[2], values[3]); err == nil {
			t.Fatalf("accepted invalid capacity %v", values)
		}
	}
}

func waitStorageSample(t *testing.T, cache *dockerStorageCache, key string, now time.Time, probe func() (dockerStorageSample, error)) (dockerStorageSample, error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		cache.mu.Lock()
		running := cache.running
		cache.mu.Unlock()
		if !running {
			return cache.get(key, now, probe)
		}
		if time.Now().After(deadline) {
			t.Fatal("background storage probe did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDockerStorageCacheDoesNotBlockOrRefreshObservationTime(t *testing.T) {
	var cache dockerStorageCache
	now := time.Now().UTC()
	release := make(chan struct{})
	var calls atomic.Int32
	probe := func() (dockerStorageSample, error) {
		calls.Add(1)
		<-release
		return dockerStorageSample{TotalBytes: 100, FreeBytes: 80, ObservedAt: now, Source: "fixture"}, nil
	}
	for i := 0; i < 20; i++ {
		if _, err := cache.get("daemon", now, probe); !errors.Is(err, errDockerStoragePending) {
			t.Fatalf("expected pending result without blocking: %v", err)
		}
	}
	close(release)
	sample, err := waitStorageSample(t, &cache, "daemon", now.Add(10*time.Second), probe)
	if err != nil || !sample.ObservedAt.Equal(now) || calls.Load() != 1 {
		t.Fatalf("probe duplicated or measurement time changed: %+v %v calls=%d", sample, err, calls.Load())
	}
	failed := func() (dockerStorageSample, error) { return dockerStorageSample{}, errors.New("drive unavailable") }
	// A failed refresh may use the previous still-fresh sample, but must never
	// relabel a stale measurement with the new heartbeat's timestamp.
	_, _ = cache.get("daemon", now.Add(70*time.Second), failed)
	sample, err = waitStorageSample(t, &cache, "daemon", now.Add(70*time.Second), failed)
	if err != nil || !sample.ObservedAt.Equal(now) {
		t.Fatalf("lost the last good observation: %+v %v", sample, err)
	}
	if _, err := cache.get("daemon", now.Add(3*time.Minute), failed); err == nil {
		t.Fatal("returned stale storage as fresh")
	}
	_, _ = waitStorageSample(t, &cache, "daemon", now.Add(3*time.Minute), failed)
}

func TestDockerStorageCacheIsolatesDaemonChanges(t *testing.T) {
	var cache dockerStorageCache
	now := time.Now().UTC()
	releaseOld := make(chan struct{})
	doneOld := make(chan struct{})
	oldProbe := func() (dockerStorageSample, error) {
		<-releaseOld
		defer close(doneOld)
		return dockerStorageSample{ObservedAt: now, Source: "old"}, nil
	}
	_, _ = cache.get("old", now, oldProbe)
	newProbe := func() (dockerStorageSample, error) {
		return dockerStorageSample{ObservedAt: now, Source: "new"}, nil
	}
	_, _ = cache.get("new", now, newProbe)
	sample, err := waitStorageSample(t, &cache, "new", now, newProbe)
	if err != nil || sample.Source != "new" {
		t.Fatalf("new daemon measurement missing: %+v %v", sample, err)
	}
	close(releaseOld)
	<-doneOld
	sample, err = cache.get("new", now, newProbe)
	if err != nil || sample.Source != "new" {
		t.Fatalf("old daemon overwrote the measurement: %+v %v", sample, err)
	}
}
