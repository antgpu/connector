package api

import (
	"reflect"
	"testing"

	nebulaConfig "github.com/slackhq/nebula/config"
)

func TestApplyNebulaMTU(t *testing.T) {
	connCfg := nebulaConfig.NewC(nil)
	(*connCfg).Settings["tun"] = map[string]any{"disabled": true}

	if err := applyNebulaMTU(connCfg, 1200); err != nil {
		t.Fatalf("applyNebulaMTU returned error: %v", err)
	}

	tun := normalizeSettingsMap((*connCfg).Settings["tun"])
	if tun["disabled"] != true {
		t.Fatalf("expected tun.disabled preserved, got %#v", tun["disabled"])
	}
	if tun["mtu"] != 1200 {
		t.Fatalf("expected tun.mtu=1200, got %#v", tun["mtu"])
	}
}

func TestMergeMappings_SystemFirstAndDeduped(t *testing.T) {
	got := mergeMappings(
		[]string{"8099:127.0.0.1:8099:both"},
		[]string{
			"8080:127.0.0.1:8080:tcp",
			"8099:127.0.0.1:8099:both",
			"8080:127.0.0.1:8080:tcp",
		},
	)

	want := []string{
		"8099:127.0.0.1:8099:both",
		"8080:127.0.0.1:8080:tcp",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected merged mappings: got=%#v want=%#v", got, want)
	}
}

func TestTunnelRuntime_EffectivePortMappings(t *testing.T) {
	runtime := NewTunnelRuntime([]string{"8099:127.0.0.1:8099:both"})
	got := runtime.EffectivePortMappings([]string{"8080:127.0.0.1:8080:tcp"})
	want := []string{
		"8099:127.0.0.1:8099:both",
		"8080:127.0.0.1:8080:tcp",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected effective mappings: got=%#v want=%#v", got, want)
	}
}

func TestTunnelRuntime_EffectivePortMappings_UsesStoredDynamicWhenNilInput(t *testing.T) {
	runtime := NewTunnelRuntime([]string{"8099:127.0.0.1:8099:both"})
	runtime.dynamicPortMapping = []string{"12434:127.0.0.1:12434:tcp"}

	got := runtime.EffectivePortMappings(nil)
	want := []string{
		"8099:127.0.0.1:8099:both",
		"12434:127.0.0.1:12434:tcp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected effective mappings with stored dynamic: got=%#v want=%#v", got, want)
	}
}
