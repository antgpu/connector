package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/antgpu/connector/internal/api"
	internaldocker "github.com/antgpu/connector/internal/docker"
	"github.com/antgpu/connector/internal/hw"
	"github.com/antgpu/connector/internal/licensing"
	"github.com/antgpu/connector/internal/runtimelog"
	"github.com/antgpu/connector/internal/updater"
	"github.com/sirupsen/logrus"
	nebulaConfig "github.com/slackhq/nebula/config"
	"gopkg.in/yaml.v2"
)

const ListenAddr = "127.0.0.1:8099"

func main() {
	opts, err := parseRuntimeOptions(os.Args[1:])
	if err != nil {
		log.Fatalf("connect: parse args: %v", err)
	}
	applyRuntimeDefaults(&opts)

	if opts.PrintVersion {
		fmt.Println(AppVersion)
		return
	}
	if opts.PrintLicenses {
		fmt.Print(licensing.Notices)
		return
	}

	if err := prepareStateWorkingDir(opts); err != nil {
		log.Fatalf("connect: %v", err)
	}
	closeRuntimeLog, err := setupRuntimeLog(opts)
	if err != nil {
		log.Fatalf("connect: setup runtime log: %v", err)
	}
	defer closeRuntimeLog()
	if err := prepareDockerCLIEnv(opts); err != nil {
		log.Printf("connect: docker env setup warning: %v", err)
	}

	if handled, err := maybeHandleWindowsServiceMode(opts); handled {
		if err != nil {
			log.Fatalf("connect: %v", err)
		}
		return
	}

	if handled, err := maybeHandleWindowsTrayMode(opts); handled {
		if err != nil {
			log.Fatalf("connect: %v", err)
		}
		return
	}

	if shouldDaemonize(opts) {
		if err := startDaemonChild(opts); err != nil {
			log.Fatalf("connect: daemonize: %v", err)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, opts, stop); err != nil {
		log.Fatalf("connect: %v", err)
	}
}

func run(ctx context.Context, opts runtimeOptions, requestShutdown func()) error {
	tokenOverride := strings.TrimSpace(opts.Token)
	if tokenOverride != "" {
		hasLocalState, err := hasPersistedEnrollmentState()
		if err != nil {
			return fmt.Errorf("check persisted enrollment state: %w", err)
		}
		if isManagedServiceRuntime(opts) && hasLocalState {
			log.Printf("connect: ignoring persisted service enrollment token because local state already exists")
			tokenOverride = ""
			if err := clearPersistedServiceEnrollmentToken(opts); err != nil {
				log.Printf("connect: clear persisted service enrollment token: %v", err)
			}
		} else {
			if err := resetEnrollmentStateForTokenOverride(); err != nil {
				return fmt.Errorf("prepare token override enrollment state: %w", err)
			}
		}
	}

	authData, err := getMachineAuthData()
	if err != nil {
		return fmt.Errorf("getting machine auth data: %w", err)
	}
	if isManagedServiceRuntime(opts) && authData != nil {
		if err := clearPersistedServiceEnrollmentToken(opts); err != nil {
			log.Printf("connect: clear persisted service enrollment token: %v", err)
		}
	}

	apiService := api.APIService{
		APIAddr:       APIAddr,
		DockerService: internaldocker.NewService(),
		StateDir:      opts.StateDir,
		Provisioning:  api.NewProvisioningManager(),
	}
	var removeOnce sync.Once
	apiService.OnConnectorRemove = func(input api.ConnectorRemoveInput) {
		removeOnce.Do(func() {
			log.Printf("connector remove: requested machine_id=%s reason=%s requested_by_user_id=%d", strings.TrimSpace(input.MachineID), strings.TrimSpace(input.Reason), input.RequestedByUserID)

			stopCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			stopResult, err := apiService.DockerService.StopAllManagedContainers(stopCtx, 20*time.Second)
			cancel()
			if err != nil {
				log.Printf("connector remove: stop managed containers warning: %v", err)
			} else {
				log.Printf("connector remove: stopped %d managed containers", len(stopResult.Stopped))
				for _, warning := range stopResult.Warnings {
					log.Printf("connector remove: stop managed container warning: %s", warning)
				}
			}

			if err := cleanupConnectorLocalState(opts); err != nil {
				log.Printf("connector remove: cleanup warning: %v", err)
			}
			if requestShutdown != nil {
				requestShutdown()
			}
		})
	}
	tunnelRuntime := api.NewTunnelRuntime(api.DefaultSystemPortMappings())
	apiService.TunnelRuntime = tunnelRuntime

	initialConnectToken := ""
	connectorLogToken := ""
	if tokenOverride != "" {
		initialConnectToken = tokenOverride
		connectorLogToken = tokenOverride
		authData = nil
	} else if authData == nil {
		if tokenOverride == "" {
			return fmt.Errorf("first time usage: %s <token>", os.Args[0])
		}
		initialConnectToken = tokenOverride
	} else {
		apiService.MachineAuthData = *authData
		connectorLogToken = authData.MachineToken
	}

	sendConnectorLog := func(level, phase, message string, fields map[string]any) {
		token := strings.TrimSpace(connectorLogToken)
		if token == "" {
			token = strings.TrimSpace(apiService.MachineAuthData.MachineToken)
		}
		if token == "" {
			return
		}
		if fields == nil {
			fields = map[string]any{}
		}
		level = strings.TrimSpace(strings.ToLower(level))
		_, hasLogTail := fields["log_tail"]
		if level == "error" || level == "fatal" {
			if !hasLogTail || strings.TrimSpace(fmt.Sprint(fields["log_tail"])) == "" {
				if tail := recentConnectorLogTail(opts.StateDir); tail != "" {
					fields["log_tail"] = tail
				}
			}
		}
		if err := apiService.SendConnectorLog(context.Background(), token, api.ConnectorLogInput{
			Level:            level,
			Phase:            phase,
			Message:          message,
			Fields:           fields,
			ConnectorVersion: AppVersion,
		}); err != nil {
			log.Printf("connector log sync warning: %v", err)
		}
	}

	l := logrus.New()
	logrusOut := newLogrusNormalizingWriter(runtimeLogOutput)
	l.SetOutput(logrusOut)
	l.SetFormatter(newStdLikeLogrusFormatter())
	logrus.SetOutput(logrusOut)
	logrus.SetFormatter(newStdLikeLogrusFormatter())
	connCfg := nebulaConfig.NewC(l)

	sendConnectorLog("info", "enroll", "connector startup started", map[string]any{"first_connect": initialConnectToken != ""})
	if err := loadOrFetchConnectionConfig(connCfg, &apiService, initialConnectToken); err != nil {
		phase := "config"
		if initialConnectToken != "" {
			phase = "enroll"
		}
		sendConnectorLog("error", phase, fmt.Sprintf("connector configuration failed: %v", err), nil)
		return err
	}
	connectorLogToken = apiService.MachineAuthData.MachineToken
	if initialConnectToken != "" {
		sendConnectorLog("info", "enroll", "connector enrollment succeeded", nil)
	} else {
		sendConnectorLog("info", "config", "machine connection config loaded", nil)
	}
	if isManagedServiceRuntime(opts) && strings.TrimSpace(opts.Token) != "" && initialConnectToken != "" {
		if err := clearPersistedServiceEnrollmentToken(opts); err != nil {
			log.Printf("connect: clear persisted service enrollment token after enrollment: %v", err)
		}
	}

	apiService.WorkloadMutex = &sync.RWMutex{}
	apiService.BenchmarkReady = &atomic.Bool{}
	connectorUpdater := updater.NewManager(updater.ManagerConfig{
		APIAddr:      APIAddr,
		MachineToken: apiService.MachineAuthData.MachineToken,
		Version:      AppVersion,
		CacheRoot:    filepath.Join("cache", "updates"),
		PublicKey:    updater.TrustedPublicKey,
		HasRunningManagedContainers: func(ctx context.Context) (bool, error) {
			if !apiService.WorkloadMutex.TryRLock() {
				return true, nil
			}
			defer apiService.WorkloadMutex.RUnlock()
			if apiService.DockerService == nil {
				return false, nil
			}
			return apiService.DockerService.HasRunningManagedContainers(ctx)
		},
		RestartBinary: makeRestartBinary(opts, requestShutdown),
		Logger:        log.Printf,
	})
	apiService.Updater = connectorUpdater
	if err := completeLegacyMigration(ctx, connectorUpdater); err != nil {
		return fmt.Errorf("complete legacy connector migration: %w", err)
	}

	runHostPreflight(ctx, &apiService, connectorUpdater)

	log.Println("Connecting to platform...")

	tunnelConnected := false
	var tunnelDone chan error
	for attempt := 1; attempt <= 3; attempt++ {
		tunnelStartCtx, cancelTunnelStart := context.WithTimeout(ctx, 30*time.Second)
		tunnelStarted := make(chan struct{}, 1)
		tunnelDone = make(chan error, 1)

		go func() {
			tunnelDone <- api.TunnelConnect(ctx, connCfg, l, tunnelRuntime, tunnelStarted)
		}()

		select {
		case <-tunnelStarted:
			cancelTunnelStart()
			log.Println("Success!")
			sendConnectorLog("info", "tunnel", "tunnel started", nil)
			connectorUpdater.SetConnected()
			tunnelConnected = true
		case err := <-tunnelDone:
			cancelTunnelStart()
			if err != nil {
				if attempt < 3 && isTunnelCertTimeError(err) {
					log.Printf("tunnel startup cert time check failed, retrying after clock settles: %v", err)
					select {
					case <-ctx.Done():
						return nil
					case <-time.After(5 * time.Second):
					}
					continue
				}
				reason := fmt.Sprintf("tunnel startup failed: %v. If you use VPN/proxy/firewall filtering, it may block UDP overlay tunnels; disable VPN or try another network/VPN endpoint.", err)
				log.Print(reason)
				sendConnectorLog("error", "tunnel", reason, nil)
				reportConnectorFailure(&apiService, connectorUpdater, reason)
				return fmt.Errorf("starting tunnel: %w", err)
			}
			reason := "tunnel stopped before startup completed. If you use VPN/proxy/firewall filtering, it may block UDP overlay tunnels; disable VPN or try another network/VPN endpoint."
			log.Print(reason)
			sendConnectorLog("error", "tunnel", reason, nil)
			reportConnectorFailure(&apiService, connectorUpdater, reason)
			return errors.New("tunnel stopped before startup completed")
		case <-tunnelStartCtx.Done():
			cancelTunnelStart()
			if ctx.Err() != nil {
				return nil
			}
			reason := "timed out waiting for tunnel to start. HTTPS enrollment worked, but UDP overlay tunnel did not become reachable. Disable VPN/proxy/firewall filtering or try another network/VPN endpoint."
			log.Print(reason)
			sendConnectorLog("error", "tunnel", reason, nil)
			reportConnectorFailure(&apiService, connectorUpdater, reason)
			return errors.New("timed out waiting for tunnel to start")
		}
		break
	}
	if !tunnelConnected {
		sendConnectorLog("error", "tunnel", "tunnel did not start", nil)
		return errors.New("tunnel did not start")
	}

	if shouldRunAutoUpdater(opts) && runPreBenchmarkUpdateCheck(ctx, connectorUpdater, opts.TestAutoUpdate) {
		return nil
	}

	specs := hw.FetchSpecs()
	benchmarkManaged := false
	if shouldEnforceBenchSuccess() {
		benchmarkManaged, err = runBenchmarkCycle(ctx, &apiService, specs, true)
	}
	if err != nil {
		return fmt.Errorf("startup benchmark policy: %w", err)
	}
	if !benchmarkManaged {
		benchCtx, cancelBench := context.WithTimeout(ctx, benchTimeout())
		benchNetCfg := hw.BenchNetworkConfig{}
		benchMode := hw.BenchRunMode{}
		if cfgData, err := apiService.GetMachineConnectionConfig(false); err != nil {
			log.Printf("get machine config for bench settings: %v", err)
		} else {
			benchNetCfg = hw.BenchNetworkConfig{
				IperfHost:    cfgData.Bench.IperfHost,
				IperfPort:    cfgData.Bench.IperfPort,
				IperfSeconds: cfgData.Bench.IperfSeconds,
			}
			benchMode = hw.BenchRunMode{
				RunGPUBase: cfgData.Bench.RunGPUBase,
				RunCompat:  cfgData.Bench.RunCompat,
				RunECC:     cfgData.Bench.RunECC,
				RunStress:  cfgData.Bench.RunStress,
				RunNetwork: cfgData.Bench.RunNetwork,
			}
		}
		// An older backend has no fingerprint-aware policy: retain full admission.
		benchMode.RunGPUBase = true
		benchMode.RunCompat = true
		benchMode.RunStress = true
		benchMode = effectiveBenchRunMode(benchMode)
		sendConnectorLog("info", "benchmark", "benchmark started", map[string]any{
			"run_gpu_base": benchMode.RunGPUBase,
			"run_compat":   benchMode.RunCompat,
			"run_ecc":      benchMode.RunECC,
			"run_stress":   benchMode.RunStress,
			"run_network":  benchMode.RunNetwork,
		})
		hw.EnrichWithBenchmarks(benchCtx, specs, benchNetCfg, benchMode)
		cancelBench()

		specs.ModelRunner = detectModelRunnerCapability(ctx, apiService.DockerService)

		benchRequested := benchMode.RunGPUBase || benchMode.RunCompat || benchMode.RunECC || benchMode.RunStress || benchMode.RunNetwork
		if benchRequested {
			if benchErr := strings.TrimSpace(specs.BenchFetchErr); benchErr != "" {
				if shouldEnforceBenchSuccess() {
					reason := fmt.Sprintf("bench failed; refusing to start connector services: %s", benchErr)
					fields := benchmarkLogFields(specs)
					fields["error"] = benchErr
					sendConnectorLog("fatal", "benchmark", "bench failed; refusing to start connector services", fields)
					return errors.New(reason)
				}
				log.Printf("bench: continuing without strict bench gate: %s", benchErr)
				fields := benchmarkLogFields(specs)
				fields["error"] = benchErr
				sendConnectorLog("warn", "benchmark", "benchmark failed but strict gate is disabled", fields)
			} else {
				sendConnectorLog("info", "benchmark", "benchmark completed", benchmarkLogFields(specs))
			}
		}

		if !benchRequested {
			sendConnectorLog("info", "benchmark", "benchmark skipped by backend policy", nil)
		}

		apiService.BenchmarkReady.Store(true)
	}
	if specs.ModelRunner == nil {
		specs.ModelRunner = detectModelRunnerCapability(ctx, apiService.DockerService)
	}
	if err := apiService.SendSpecs(*specs); err != nil {
		sendConnectorLog("error", "hardware-specs", fmt.Sprintf("send specs failed: %v", err), nil)
		return fmt.Errorf("send specs: %w", err)
	}
	sendConnectorLog("info", "service-start", "connector passed startup checks", nil)

	server := &http.Server{
		Addr:              ListenAddr,
		Handler:           apiService.WebService(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 8)
	var wg sync.WaitGroup

	startManaged(&wg, errCh, "api server", func() error {
		log.Printf("listening on %s", server.Addr)
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})

	startManaged(&wg, errCh, "health reporter", func() error {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for {
			if err := apiService.ReportHealth(); err != nil {
				log.Printf("report health: %v", err)
				connectorUpdater.SetDisconnected("health_report_failed")
			} else {
				if connectorUpdater.Snapshot().State != string(updater.StateUpdating) {
					connectorUpdater.SetConnected()
				}
			}

			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	})

	if benchmarkManaged {
		startManaged(&wg, errCh, "benchmark scheduler", func() error { return runBenchmarkScheduler(ctx, &apiService) })
	}
	startManaged(&wg, errCh, "storage diagnostic", func() error { return runStorageDiagnostic(ctx, &apiService) })
	startManaged(&wg, errCh, "specs refresher", func() error {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if err := apiService.RefreshSpecs("periodic"); err != nil {
					log.Printf("refresh hardware specs: %v", err)
				}
			}
		}
	})

	if shouldRunAutoUpdater(opts) {
		startManaged(&wg, errCh, "connector updater", func() error {
			if opts.TestAutoUpdate {
				updateCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
				result, err := connectorUpdater.RequestUpdate(updateCtx, updater.Request{Force: true})
				cancel()
				if err != nil {
					log.Printf("connector updater test startup request failed: %v", err)
				} else {
					log.Printf("connector updater test startup request result: accepted=%t deferred=%t already_latest=%t target=%s reason=%s", result.Accepted, result.Deferred, result.AlreadyLatest, result.TargetVersion, result.Reason)
				}
			}

			delay := 2 * time.Minute
			lastError := ""

			for {
				updateCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
				_, err := connectorUpdater.Poll(updateCtx)
				cancel()
				if err != nil {
					if err.Error() != lastError {
						log.Printf("connector updater poll: %v", err)
					}
					lastError = err.Error()
				} else {
					lastError = ""
				}
				delay = nextUpdaterPollDelay(delay, err != nil)
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil
				case <-timer.C:
				}
			}
		})
	}

	var runErr error

	select {
	case <-ctx.Done():
	case err := <-tunnelDone:
		if err != nil {
			runErr = fmt.Errorf("tunnel error: %w", err)
			connectorUpdater.SetDisconnected("tunnel_error")
		}
	case err := <-errCh:
		runErr = err
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()

	_ = server.Shutdown(shutdownCtx)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-shutdownCtx.Done():
		if runErr == nil {
			runErr = errors.New("shutdown timed out")
		}
	}

	if runErr != nil {
		return runErr
	}

	return nil
}

func runHostPreflight(ctx context.Context, apiService *api.APIService, connectorUpdater *updater.Manager) {
	if apiService == nil || apiService.DockerService == nil {
		return
	}

	preflightCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	dockerStatus := apiService.DockerService.CheckAvailability(preflightCtx)
	if !dockerStatus.DaemonAvailable {
		reason := strings.TrimSpace(dockerStatus.Error)
		if reason == "" {
			reason = "Docker is unavailable. Install Docker and start the Docker daemon before renting workloads."
		}
		log.Printf("preflight: %s", reason)
		reportConnectorFailure(apiService, connectorUpdater, reason)
	} else {
		log.Print("preflight: Docker daemon reachable")
	}

	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		reason := "nvidia-smi not found. NVIDIA driver may be missing; GPU workloads and benchmarks may fail until driver is installed."
		log.Printf("preflight: %s", reason)
		reportConnectorFailure(apiService, connectorUpdater, reason)
	} else {
		log.Print("preflight: nvidia-smi found")
	}
}

func reportConnectorFailure(apiService *api.APIService, connectorUpdater *updater.Manager, reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	if connectorUpdater != nil {
		connectorUpdater.SetDisconnected(reason)
	}
	if apiService == nil {
		return
	}
	if err := apiService.ReportHealth(); err != nil {
		log.Printf("report connector failure: %v", err)
	}
}

func detectModelRunnerCapability(ctx context.Context, dockerService *internaldocker.Service) *hw.ModelRunnerCapability {
	capability := &hw.ModelRunnerCapability{
		Supported: false,
	}
	if dockerService == nil {
		capability.LastError = "docker service unavailable"
		return capability
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	dockerStatus := dockerService.CheckAvailability(probeCtx)
	if !dockerStatus.DaemonAvailable {
		capability.LastError = strings.TrimSpace(dockerStatus.Error)
		if capability.LastError == "" {
			capability.LastError = "Docker is unavailable. Install Docker and start the Docker daemon."
		}
		return capability
	}

	if !dockerService.IsModelRunnerCommandAvailable(probeCtx) {
		capability.LastError = "docker model command unavailable (install Docker CE and docker-model-plugin)"
		return capability
	}
	capability.Supported = true

	if err := dockerService.EnsureModelRunnerEnabled(probeCtx); err != nil {
		log.Printf("model runner: auto-enable warning: %v", err)
	}

	st := dockerService.ModelRunnerStatus(probeCtx)
	capability.Enabled = st.Available
	capability.BaseURL = strings.TrimSpace(st.BaseURL)
	capability.LastError = strings.TrimSpace(st.Error)

	if !st.Available {
		capability.Healthy = false
		return capability
	}

	_, listStatus, err := dockerService.ModelRunnerListModels(probeCtx)
	if err != nil {
		capability.Healthy = false
		if msg := strings.TrimSpace(listStatus.Error); msg != "" {
			capability.LastError = msg
		} else {
			capability.LastError = strings.TrimSpace(err.Error())
		}
		return capability
	}

	capability.Healthy = true
	capability.LastError = ""
	return capability
}

func cleanupConnectorLocalState(opts runtimeOptions) error {
	var errs []string

	paths := []string{
		MachineAuthPath,
		TunnelConfigPath,
		".auth",
		".auth-dev",
		"conn.yaml",
		"conn-dev.yaml",
		"connect.log",
		showLogsOnceMarker,
	}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if err := removeIfExists(p); err != nil {
			errs = append(errs, fmt.Sprintf("remove %s: %v", p, err))
		}
	}

	rotatedLogs, err := filepath.Glob("connect-old*.log")
	if err != nil {
		errs = append(errs, fmt.Sprintf("glob rotated logs: %v", err))
		rotatedLogs = nil
	}
	for _, p := range rotatedLogs {
		if err := removeIfExists(p); err != nil {
			errs = append(errs, fmt.Sprintf("remove %s: %v", p, err))
		}
	}
	if err := os.RemoveAll("docker-config"); err != nil {
		errs = append(errs, fmt.Sprintf("remove docker-config: %v", err))
	}

	if err := cleanupConnectorPlatformHooks(opts); err != nil {
		log.Printf("connector remove: platform cleanup warning: %v", err)
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func resetEnrollmentStateForTokenOverride() error {
	paths := []string{
		MachineAuthPath,
		TunnelConfigPath,
	}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if err := removeIfExists(p); err != nil {
			return err
		}
	}
	return nil
}

func isManagedServiceRuntime(opts runtimeOptions) bool {
	if opts.RunningAsService {
		return true
	}
	return strings.TrimSpace(os.Getenv("INVOCATION_ID")) != ""
}

func hasPersistedEnrollmentState() (bool, error) {
	paths := []string{MachineAuthPath, TunnelConfigPath}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		_, err := os.Stat(p)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func shouldRunAutoUpdater(runtimeOptions) bool {
	return true
}

func runPreBenchmarkUpdateCheck(ctx context.Context, connectorUpdater *updater.Manager, force bool) bool {
	if connectorUpdater == nil {
		return false
	}

	updateCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	result, err := connectorUpdater.RequestUpdate(updateCtx, updater.Request{Force: force})
	cancel()
	if err != nil {
		log.Printf("connector updater pre-bench check failed: %v", err)
		return false
	}

	if result.AlreadyLatest {
		log.Printf("connector updater pre-bench check: already latest version=%s", result.Version)
		return false
	}
	if result.Deferred {
		log.Printf("connector updater pre-bench check deferred: target=%s reason=%s", result.TargetVersion, result.Reason)
		return false
	}
	if !result.Accepted {
		log.Printf("connector updater pre-bench check skipped: target=%s reason=%s", result.TargetVersion, result.Reason)
		return false
	}

	log.Printf("connector updater pre-bench update accepted: current=%s target=%s; waiting for replacement before benchmark", result.Version, result.TargetVersion)
	return waitForPreBenchmarkUpdateHandoff(ctx, connectorUpdater, 15*time.Second)
}

func waitForPreBenchmarkUpdateHandoff(ctx context.Context, connectorUpdater *updater.Manager, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return true
		case <-deadline.C:
			log.Printf("connector updater pre-bench handoff timeout; stopping current process before benchmark")
			return true
		case <-ticker.C:
			snap := connectorUpdater.Snapshot()
			if snap.State != string(updater.StateUpdating) {
				if strings.TrimSpace(snap.LastError) != "" {
					log.Printf("connector updater pre-bench update failed: %s", strings.TrimSpace(snap.LastError))
				} else {
					log.Printf("connector updater pre-bench update stopped unexpectedly")
				}
				return false
			}
		}
	}
}

func benchTimeout() time.Duration {
	value := strings.TrimSpace(os.Getenv("CONNECT_BENCH_TIMEOUT"))
	if value == "" {
		return 2 * time.Hour
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		log.Printf("bench: invalid CONNECT_BENCH_TIMEOUT=%q, using 2h", value)
		return 2 * time.Hour
	}
	return d
}

func benchmarkLogFields(specs *hw.SystemSpecs) map[string]any {
	fields := map[string]any{
		"gpu_total_measured_tflops": specs.TotalMeasuredTFLOPS,
		"gpu_total_spec_tflops":     specs.TotalSpecTFLOPS,
		"gpu_workload":              specs.GPUWorkload,
		"gpu_tensor":                specs.GPUTensor,
	}
	if specs.NetDownMbps > 0 {
		fields["net_down_mbps"] = specs.NetDownMbps
	}
	if specs.NetUpMbps > 0 {
		fields["net_up_mbps"] = specs.NetUpMbps
	}
	return fields
}

func shouldEnforceBenchSuccess() bool {
	if !devBuild {
		return true
	}

	// Bench container is linux-focused; don't hard-fail local mac dev runs.
	if runtime.GOOS == "darwin" {
		return false
	}

	// `go run -tags dev` uses a temp go-build executable path.
	if isGoRunBinary() {
		return false
	}

	return true
}

func effectiveBenchRunMode(mode hw.BenchRunMode) hw.BenchRunMode {
	return hw.HardenBenchRunMode(mode)
}

func isGoRunBinary() bool {
	return isGoRunBinaryPath(executablePath())
}

func executablePath() string {
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	return exePath
}

func isGoRunBinaryPath(exePath string) bool {
	exePath = filepath.ToSlash(exePath)
	return strings.Contains(exePath, "/go-build") && strings.Contains(exePath, "/exe/")
}

func recentConnectorLogTail(stateDir string) string {
	path := runtimelog.ResolvePrimaryPath(stateDir)
	result, err := runtimelog.Read(path, runtimelog.ReadOptions{Tail: 120, Limit: 32 * 1024})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result.Content)
}

func startManaged(wg *sync.WaitGroup, errCh chan<- error, name string, fn func() error) {
	wg.Go(func() {
		if err := fn(); err != nil {
			select {
			case errCh <- fmt.Errorf("%s: %w", name, err):
			default:
			}
		}
	})
}

func isTunnelCertTimeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "certificate") && strings.Contains(msg, "expired")
}

func loadOrFetchConnectionConfig(connCfg *nebulaConfig.C, apiService *api.APIService, token string) error {
	hasAuth := apiService.MachineAuthData.MachineID != "" && apiService.MachineAuthData.MachineToken != ""

	if hasAuth {
		log.Println("Fetching latest machine connection config from API...")
		cfgData, err := apiService.GetMachineConnectionConfig(false)
		if err == nil {
			connCfg.LoadString(cfgData.ConnectionConfig)
			if err := saveConnectionConfig(connCfg); err != nil {
				return err
			}
			return nil
		}

		if _, statErr := os.Stat(TunnelConfigPath); statErr == nil {
			log.Printf("get machine config failed, using local %s: %v", TunnelConfigPath, err)
			connCfg.Load(TunnelConfigPath)
			return nil
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("stat %s: %w", TunnelConfigPath, statErr)
		}

		return fmt.Errorf("get machine config: %w", err)
	}

	if _, err := os.Stat(TunnelConfigPath); os.IsNotExist(err) {
		log.Println("First connect, registering machine...")

		firstConnectData, err := apiService.FetchConnectionConfig(token)
		if err != nil {
			return fmt.Errorf("first connect: %w", err)
		}

		connCfg.LoadString(firstConnectData.ConnectionConfig)
		apiService.MachineAuthData = firstConnectData.MachineAuthData

		if err := saveConnectionConfig(connCfg); err != nil {
			return err
		}

		machineAuthBytes, err := json.Marshal(firstConnectData.MachineAuthData)
		if err != nil {
			return fmt.Errorf("marshal machine auth: %w", err)
		}
		if err := os.WriteFile(MachineAuthPath, machineAuthBytes, 0o600); err != nil {
			return fmt.Errorf("save machine auth to %s: %w", MachineAuthPath, err)
		}
		_ = os.Chmod(MachineAuthPath, 0o600)
		return nil
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", TunnelConfigPath, err)
	}

	connCfg.Load(TunnelConfigPath)
	return nil
}

func saveConnectionConfig(connCfg *nebulaConfig.C) error {
	connCfgBytes, err := yaml.Marshal(connCfg.Settings)
	if err != nil {
		return fmt.Errorf("marshal yaml conn cfg: %w", err)
	}
	if err := os.WriteFile(TunnelConfigPath, connCfgBytes, 0o600); err != nil {
		return fmt.Errorf("save conn cfg to %s: %w", TunnelConfigPath, err)
	}
	_ = os.Chmod(TunnelConfigPath, 0o600)

	return nil
}

func getMachineAuthData() (*api.MachineAuthData, error) {
	_, err := os.Stat(MachineAuthPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat machine auth data: %w", err)
	}

	data, err := os.ReadFile(MachineAuthPath)
	if err != nil {
		return nil, fmt.Errorf("read machine auth data: %w", err)
	}

	var machineAuthData api.MachineAuthData
	if err := json.Unmarshal(data, &machineAuthData); err != nil {
		return nil, fmt.Errorf("unmarshal json machine auth data: %w", err)
	}

	return &machineAuthData, nil
}

func nextUpdaterPollDelay(previous time.Duration, failed bool) time.Duration {
	if !failed {
		return 2 * time.Minute
	}
	next := previous * 2
	if next > 15*time.Minute {
		next = 15 * time.Minute
	}
	return next
}
