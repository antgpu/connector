package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/antgpu/connector/internal/api"
	"github.com/antgpu/connector/internal/hw"
)

func benchmarkMode(p api.BenchmarkPolicy) hw.BenchRunMode {
	return hw.BenchRunMode{RunGPUBase: p.Groups["gpu_base"].Run, RunCompat: p.Groups["compat"].Run, RunStress: p.Groups["stress"].Run, RunNetwork: p.Groups["network"].Run}
}

// runBenchmarkCycle uses backend validity rather than a local timestamp cache.
// The exclusive lock covers provisioning, readiness checks and lease release.
type benchmarkHostChecks struct {
	identity    func(context.Context, *hw.SystemSpecs) (string, string, error)
	running     func(context.Context) (bool, error)
	readiness   func(context.Context) error
	run         func(context.Context, *hw.SystemSpecs, hw.BenchNetworkConfig, hw.BenchRunMode)
	modelRunner func(context.Context) *hw.ModelRunnerCapability
	renewEvery  time.Duration
	report      func(string, string, map[string]any)
}

func runBenchmarkCycle(ctx context.Context, s *api.APIService, specs *hw.SystemSpecs, startup bool) (bool, error) {
	report := func(level, message string, fields map[string]any) {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := s.SendConnectorLog(c, s.MachineAuthData.MachineToken, api.ConnectorLogInput{Level: level, Phase: "benchmark", Message: message, Fields: fields, ConnectorVersion: AppVersion}); err != nil {
			log.Printf("report benchmark log: %v", err)
		}
	}
	managed, err := runBenchmarkCycleWithChecks(ctx, s, specs, startup, benchmarkHostChecks{
		identity: hw.ProbeBenchmarkIdentity, running: s.DockerService.HasRunningManagedContainers, readiness: hw.QuickBenchmarkReadiness, run: hw.EnrichWithBenchmarks,
		modelRunner: func(c context.Context) *hw.ModelRunnerCapability {
			return detectModelRunnerCapability(c, s.DockerService)
		}, renewEvery: 25 * time.Second, report: report,
	})
	if err != nil {
		report("error", "benchmark policy failed", map[string]any{"error": err.Error()})
	}
	return managed, err
}
func runBenchmarkCycleWithChecks(ctx context.Context, s *api.APIService, specs *hw.SystemSpecs, startup bool, checks benchmarkHostChecks) (bool, error) {
	s.WorkloadMutex.Lock()
	defer s.WorkloadMutex.Unlock()
	probeCtx, cancelProbe := context.WithTimeout(ctx, 30*time.Second)
	gpu, network, err := checks.identity(probeCtx, specs)
	cancelProbe()
	if err != nil {
		s.BenchmarkReady.Store(false)
		if ctx.Err() == nil {
			_ = s.BenchmarkReadinessFailure(ctx, err.Error())
		}
		return true, err
	}
	p, err := s.BenchmarkPlan(ctx, api.BenchmarkIdentity{GPU: gpu, Network: network})
	if errors.Is(err, api.ErrBenchmarkUnsupported) {
		return false, nil
	}
	if err != nil {
		s.BenchmarkReady.Store(false)
		return true, fmt.Errorf("request benchmark policy: %w", err)
	}
	if err = validateBenchmarkPolicy(p); err != nil {
		s.BenchmarkReady.Store(false)
		return true, err
	}
	specs.BenchmarkProtocol = 1
	s.BenchmarkReady.Store(p.Ready || (p.Deferred && p.Reason == "active_rental"))
	if p.Deferred {
		if startup && checks.report != nil {
			checks.report("info", "benchmark revalidation deferred", map[string]any{"reason": p.Reason})
		}
		log.Printf("benchmark revalidation deferred: %s", p.Reason)
		return true, nil
	}
	if p.Ready {
		if startup {
			running, e := checks.running(ctx)
			if e != nil {
				s.BenchmarkReady.Store(false)
				_ = s.BenchmarkReadinessFailure(ctx, e.Error())
				return true, e
			}
			if !running {
				probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
				err = checks.readiness(probeCtx)
				cancel()
				if err != nil {
					s.BenchmarkReady.Store(false)
					_ = s.BenchmarkReadinessFailure(ctx, err.Error())
					return true, err
				}
			}
			log.Printf("benchmark: reusing valid results (trusted=%t); readiness checks passed", p.Trusted)
			if checks.report != nil {
				checks.report("info", "cached benchmark results reused; readiness checks passed", map[string]any{"trusted": p.Trusted})
			}
		}
		return true, nil
	}
	if p.LeaseID == "" {
		s.BenchmarkReady.Store(false)
		return true, fmt.Errorf("backend did not reserve pending benchmarks")
	}
	benchCtx, cancel := context.WithTimeout(ctx, benchTimeout())
	defer cancel()
	var renew sync.WaitGroup
	renew.Add(1)
	go func() {
		defer renew.Done()
		ticker := time.NewTicker(checks.renewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-benchCtx.Done():
				return
			case <-ticker.C:
				if err := s.BenchmarkRenew(benchCtx, p.LeaseID); err != nil {
					log.Printf("benchmark reservation lost: %v", err)
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); renew.Wait() }()
	// A second check follows the authoritative reservation. No active rental
	// can now start; unmanaged local activity still causes a safe deferral.
	running, runErr := checks.running(benchCtx)
	if runErr != nil || running {
		message := "managed workload is running"
		if runErr != nil {
			message = runErr.Error()
		}
		_, _ = s.BenchmarkComplete(ctx, p.LeaseID, nil, message)
		return true, nil
	}
	cfg, err := s.GetMachineConnectionConfigContext(benchCtx, false)
	if err != nil {
		_, _ = s.BenchmarkComplete(ctx, p.LeaseID, nil, err.Error())
		return true, err
	}
	mode := benchmarkMode(p)
	if checks.report != nil {
		checks.report("info", "benchmark started by backend policy", map[string]any{"trusted": p.Trusted, "run_gpu_base": mode.RunGPUBase, "run_compat": mode.RunCompat, "run_stress": mode.RunStress, "run_network": mode.RunNetwork})
	}
	log.Printf("benchmark: reserved checks trusted=%t mode=%+v", p.Trusted, mode)
	checks.run(benchCtx, specs, hw.BenchNetworkConfig{IperfHost: cfg.Bench.IperfHost, IperfPort: cfg.Bench.IperfPort, IperfSeconds: cfg.Bench.IperfSeconds}, mode)
	retainRequestedBenchmarkResults(specs, mode)
	specs.ModelRunner = checks.modelRunner(benchCtx)
	message := specs.BenchFetchErr
	if benchCtx.Err() != nil {
		message = benchCtx.Err().Error()
		specs = nil
	}
	cancel()
	renew.Wait()
	result, err := s.BenchmarkComplete(ctx, p.LeaseID, specs, message)
	if err != nil {
		return true, fmt.Errorf("submit benchmark results: %w", err)
	}
	s.BenchmarkReady.Store(result.Ready)
	if !result.Ready {
		return true, fmt.Errorf("benchmark validation remains pending: %s", message)
	}
	log.Printf("benchmark: successful results saved; next startup can reuse them")
	if checks.report != nil && specs != nil {
		checks.report("info", "benchmark completed", benchmarkLogFields(specs))
	}
	return true, nil
}

// Older pinned images also emit auxiliary GPU metrics during compatibility or
// stress runs. Never publish those as a fresh performance measurement.
func retainRequestedBenchmarkResults(s *hw.SystemSpecs, m hw.BenchRunMode) {
	if !m.RunGPUBase {
		s.PerGPUTFLOPS = nil
		s.PerGPUSpecTFLOPS = nil
		s.PerGPUMemBWGBPS = nil
		s.TotalMeasuredTFLOPS = 0
		s.TotalSpecTFLOPS = 0
		s.GPUWorkload = nil
		s.GPUTensor = nil
	}
	if !m.RunCompat {
		s.GPUCompat = nil
	}
	if !m.RunStress {
		s.GPUStress = nil
	}
	s.GPUECC = nil
	if !m.RunNetwork {
		s.NetDownMbps = 0
		s.NetUpMbps = 0
	}
}

func validateBenchmarkPolicy(p api.BenchmarkPolicy) error {
	if !p.Enabled {
		return fmt.Errorf("backend returned a disabled benchmark policy")
	}
	anyRun := false
	for _, name := range []string{"gpu_base", "compat", "stress", "network"} {
		g, ok := p.Groups[name]
		if !ok {
			return fmt.Errorf("backend omitted benchmark group %s", name)
		}
		anyRun = anyRun || g.Run
	}
	if p.Ready == anyRun {
		return fmt.Errorf("backend returned inconsistent benchmark readiness")
	}
	return nil
}

func runBenchmarkScheduler(ctx context.Context, s *api.APIService) error {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := runBenchmarkCycle(ctx, s, hw.FetchSpecs(), false); err != nil {
				log.Printf("scheduled benchmark revalidation: %v", err)
			}
		}
	}
}
