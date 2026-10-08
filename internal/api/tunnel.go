package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/mintyleaf/tunnel/pkg/configurer"
	"github.com/sirupsen/logrus"

	"github.com/slackhq/nebula"
	nebulaConfig "github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/overlay"
	"github.com/slackhq/nebula/port_forwarder"
	"github.com/slackhq/nebula/service"
)

const (
	defaultNebulaListenAddr = "0.0.0.0:4243"
	defaultNebulaMTU        = 1000
)

var defaultSystemPortMappings = []string{
	"8099:127.0.0.1:8099:both",
}

type TunnelRuntime struct {
	mu sync.Mutex

	systemPortMappings []string
	dynamicPortMapping []string
	lastApplied        []string

	connCfg    *nebulaConfig.C
	pfService  *port_forwarder.PortForwardingService
	readyState bool
}

func DefaultSystemPortMappings() []string {
	return append([]string(nil), defaultSystemPortMappings...)
}

func NewTunnelRuntime(systemPortMappings []string) *TunnelRuntime {
	if len(systemPortMappings) == 0 {
		systemPortMappings = defaultSystemPortMappings
	}
	return &TunnelRuntime{
		systemPortMappings: dedupeMappings(systemPortMappings),
	}
}

func (r *TunnelRuntime) EffectivePortMappings(dynamicMappings []string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if dynamicMappings == nil {
		dynamicMappings = r.dynamicPortMapping
	}
	return mergeMappings(r.systemPortMappings, dynamicMappings)
}

func (r *TunnelRuntime) LastAppliedPortMappings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.lastApplied...)
}

func (r *TunnelRuntime) SetReady(connCfg *nebulaConfig.C, pfService *port_forwarder.PortForwardingService, initialMappings []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.connCfg = connCfg
	r.pfService = pfService
	r.readyState = connCfg != nil && pfService != nil
	r.lastApplied = dedupeMappings(initialMappings)
}

func (r *TunnelRuntime) ApplyDynamicPortMappings(dynamicMappings []string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.readyState || r.connCfg == nil || r.pfService == nil {
		return nil, fmt.Errorf("tunnel port forwarder is not ready")
	}

	r.dynamicPortMapping = dedupeMappings(dynamicMappings)
	effective := mergeMappings(r.systemPortMappings, r.dynamicPortMapping)
	if err := configurer.ApplyPortMappings(r.connCfg, effective); err != nil {
		return nil, fmt.Errorf("apply port mappings: %w", err)
	}
	if err := r.pfService.ReloadConfigAndApplyChanges(r.connCfg); err != nil {
		return nil, fmt.Errorf("reload port forwarder: %w", err)
	}

	r.lastApplied = append([]string(nil), effective...)
	return append([]string(nil), effective...), nil
}

func mergeMappings(systemMappings []string, dynamicMappings []string) []string {
	merged := make([]string, 0, len(systemMappings)+len(dynamicMappings))
	merged = append(merged, systemMappings...)
	merged = append(merged, dynamicMappings...)
	return dedupeMappings(merged)
}

func dedupeMappings(mappings []string) []string {
	seen := make(map[string]struct{}, len(mappings))
	out := make([]string, 0, len(mappings))
	for _, raw := range mappings {
		mapping := strings.TrimSpace(raw)
		if mapping == "" {
			continue
		}
		if _, ok := seen[mapping]; ok {
			continue
		}
		seen[mapping] = struct{}{}
		out = append(out, mapping)
	}
	return out
}

type MachineEnrollConnectOutput struct {
	ConnectionConfig string            `json:"connection_config"`
	Bench            BenchConfigOutput `json:"bench"`
	MachineAuthData
}

type MachineConfigGetOutput struct {
	ConnectionConfig string            `json:"connection_config"`
	Bench            BenchConfigOutput `json:"bench"`
}

type BenchConfigOutput struct {
	IperfHost    string `json:"iperf_host"`
	IperfPort    int    `json:"iperf_port"`
	IperfSeconds int    `json:"iperf_seconds"`
	RunGPUBase   bool   `json:"run_gpu_base"`
	RunCompat    bool   `json:"run_compat"`
	RunECC       bool   `json:"run_ecc"`
	RunStress    bool   `json:"run_stress"`
	RunNetwork   bool   `json:"run_network"`
}

func (s APIService) FetchConnectionConfig(token string) (MachineEnrollConnectOutput, error) {
	var output MachineEnrollConnectOutput

	req, err := http.NewRequest(http.MethodGet, s.APIAddr+"/machines/connect", nil)
	if err != nil {
		return output, fmt.Errorf("creating http request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{}
	resp, err := client.Do(req)

	if err != nil {
		return output, fmt.Errorf("making http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return output, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return output, fmt.Errorf("non-OK status code: %d\nresponse Body: %s", resp.StatusCode, body)
	}

	if err := json.Unmarshal(body, &output); err != nil {
		return output, fmt.Errorf("unmarshaling JSON response: %w\nresponse Body: %s", err, body)
	}

	return output, nil
}

func (s APIService) GetMachineConnectionConfig(force bool) (MachineConfigGetOutput, error) {
	var output MachineConfigGetOutput

	req, err := http.NewRequest(http.MethodGet, s.APIAddr+"/machines/config", nil)
	if err != nil {
		return output, fmt.Errorf("creating http request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.MachineAuthData.MachineToken)

	q := req.URL.Query()
	if force {
		q.Set("force", "true")
	}
	req.URL.RawQuery = q.Encode()

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return output, fmt.Errorf("making http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return output, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return output, fmt.Errorf("non-OK status code: %d", resp.StatusCode)
	}

	if err := json.Unmarshal(body, &output); err != nil {
		return output, fmt.Errorf("unmarshaling JSON response: %w\nresponse Body: %s", err, body)
	}

	return output, nil
}

func normalizeSettingsMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[interface{}]interface{}:
		normalized := make(map[string]any, len(typed))
		for key, val := range typed {
			keyString, ok := key.(string)
			if !ok {
				continue
			}
			normalized[keyString] = val
		}
		return normalized
	default:
		return map[string]any{}
	}
}

func applyNebulaMTU(connCfg *nebulaConfig.C, mtu int) error {
	if connCfg == nil {
		return fmt.Errorf("connection config is nil")
	}
	if mtu <= 0 {
		return nil
	}
	settings := normalizeSettingsMap((*connCfg).Settings)
	tun := normalizeSettingsMap(settings["tun"])
	tun["mtu"] = mtu
	settings["tun"] = tun
	(*connCfg).Settings = settings
	return nil
}

func nebulaMTU() int {
	raw := strings.TrimSpace(os.Getenv("NEBULA_MTU"))
	if raw == "" {
		return defaultNebulaMTU
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return defaultNebulaMTU
	}
	return parsed
}

func TunnelConnect(ctx context.Context, connCfg *nebulaConfig.C, l *logrus.Logger, runtime *TunnelRuntime, started chan struct{}) error {
	err := configurer.ApplyListen(connCfg, defaultNebulaListenAddr)
	if err != nil {
		return fmt.Errorf("apply listen params: %w", err)
	}
	if err := applyNebulaMTU(connCfg, nebulaMTU()); err != nil {
		return fmt.Errorf("apply nebula mtu: %w", err)
	}

	initialMappings := DefaultSystemPortMappings()
	if runtime != nil {
		initialMappings = runtime.EffectivePortMappings(nil)
	}

	err = configurer.ApplyPortMappings(connCfg, initialMappings)
	if err != nil {
		return fmt.Errorf("apply port mappings: %w", err)
	}

	settings := normalizeSettingsMap(connCfg.Settings)
	settings["sshd"] = map[string]any{"enabled": false}
	connCfg.Settings = settings
	ctrl, err := nebula.Main(connCfg, false, "tunnel", l, overlay.NewUserDeviceFromConfig)
	if err != nil {
		return fmt.Errorf("nebula main: %w", err)
	}
	// Nebula sets its own TextFormatter during Main startup.
	// Re-apply our formatter so runtime logs remain consistent.
	enforceConnectorLogrusFormatter(l)

	service, err := service.New(ctrl)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}

	fwdList := port_forwarder.NewPortForwardingList()
	if err := port_forwarder.ParseConfig(l, connCfg, fwdList); err != nil {
		return fmt.Errorf("parse port forwarder config: %v", err)
	}
	pfService, err := port_forwarder.ConstructFromInitialFwdList(service, l, &fwdList)
	if err != nil {
		return fmt.Errorf("construct pf service: %w", err)
	}

	if err := pfService.Activate(); err != nil {
		return fmt.Errorf("activate pf service: %w", err)
	}

	if runtime != nil {
		runtime.SetReady(connCfg, pfService, initialMappings)
	}

	if started != nil {
		started <- struct{}{}
	}

	l.Info("Running, press ctrl+c to shutdown...")
	<-ctx.Done()

	service.CloseAndWait()

	return nil
}
