package hw

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var errDockerStoragePending = errors.New("Docker storage measurement is pending; wait for the next heartbeat")

type dockerStorageSample struct {
	TotalBytes uint64
	FreeBytes  uint64
	UsedBytes  uint64
	ObservedAt time.Time
	Source     string
}

// Windows filesystem and registry calls can block on an unavailable drive.
// Refresh outside heartbeat/provisioning requests, keep the original observation
// time, and fail closed when the last successful measurement becomes stale.
type dockerStorageCache struct {
	mu          sync.Mutex
	key         string
	running     bool
	nextAttempt time.Time
	sample      dockerStorageSample
	err         error
	generation  uint64
}

func (c *dockerStorageCache) get(key string, now time.Time, probe func() (dockerStorageSample, error)) (dockerStorageSample, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != key {
		c.key, c.running, c.nextAttempt, c.sample, c.err = key, false, time.Time{}, dockerStorageSample{}, nil
		c.generation++
	}
	if !c.running && !now.Before(c.nextAttempt) {
		c.running = true
		generation := c.generation
		go func() {
			sample, err := probe()
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.key != key || c.generation != generation {
				return
			}
			c.running, c.err = false, err
			if err == nil {
				c.sample = sample
				c.nextAttempt = now.Add(time.Minute)
			} else {
				c.nextAttempt = now.Add(30 * time.Second)
			}
		}()
	}
	age := now.Sub(c.sample.ObservedAt)
	if !c.sample.ObservedAt.IsZero() && age >= -5*time.Second && age <= 2*time.Minute {
		return c.sample, nil
	}
	if c.err != nil {
		return dockerStorageSample{}, c.err
	}
	return dockerStorageSample{}, errDockerStoragePending
}

func boundedDockerStorage(hostTotal, hostFree, guestTotal, guestAvailable uint64) (dockerStorageSample, error) {
	if hostTotal == 0 || guestTotal == 0 || hostFree > hostTotal || guestAvailable > guestTotal {
		return dockerStorageSample{}, fmt.Errorf("Docker storage capacity measurements are invalid")
	}
	total, free := min(hostTotal, guestTotal), min(hostFree, guestAvailable)
	if free > total {
		return dockerStorageSample{}, fmt.Errorf("Docker storage capacity measurements are inconsistent")
	}
	return dockerStorageSample{TotalBytes: total, FreeBytes: free, UsedBytes: total - free}, nil
}
