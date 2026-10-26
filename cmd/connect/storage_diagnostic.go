package main

import (
	"context"
	"log"
	"runtime"
	"strings"
	"time"

	"github.com/antgpu/connector/internal/api"
	"github.com/antgpu/connector/internal/hw"
)

func storageDiagnosticEnabled(version, goos string) bool {
	return goos == "windows" && strings.Contains(version, "-storage-diag.")
}

// Opted-in signed canaries emit one shadow report per process. They never write
// storage telemetry or change rental admission, Docker settings, or WSL state.
func runStorageDiagnostic(ctx context.Context, s *api.APIService) error {
	if !storageDiagnosticEnabled(AppVersion, runtime.GOOS) {
		return nil
	}
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	for {
		if s.WorkloadMutex.TryLock() {
			check, cancel := context.WithTimeout(ctx, 3*time.Second)
			busy, err := s.DockerService.HasRunningManagedContainers(check)
			cancel()
			if err == nil && !busy {
				probe, cancel := context.WithTimeout(ctx, 45*time.Second)
				report := hw.CollectStorageDiagnostic(probe)
				cancel()
				s.WorkloadMutex.Unlock()
				for attempt := 0; attempt < 3; attempt++ {
					err := s.SendConnectorLog(ctx, s.MachineAuthData.MachineToken, api.ConnectorLogInput{
						Level: "info", Phase: "storage-diagnostic", Message: "Storage discovery shadow report; rental eligibility unchanged",
						ConnectorVersion: AppVersion, Fields: hw.StorageDiagnosticFields(report),
					})
					if err == nil {
						return nil
					}
					log.Printf("storage diagnostic: report delivery failed (attempt %d)", attempt+1)
					select {
					case <-ctx.Done():
						return nil
					case <-time.After(5 * time.Second):
					}
				}
				return nil
			}
			s.WorkloadMutex.Unlock()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			log.Print("storage diagnostic: skipped because managed workload or benchmark remained busy")
			return nil
		case <-time.After(15 * time.Second):
		}
	}
}
