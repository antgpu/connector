package protocol

import "testing"

func TestParsePortSpec_DefaultTCP(t *testing.T) {
	spec, err := ParsePortSpec("8080")
	if err != nil {
		t.Fatalf("ParsePortSpec returned error: %v", err)
	}
	if spec.ContainerPort != 8080 {
		t.Fatalf("expected container_port=8080, got %d", spec.ContainerPort)
	}
	if spec.HostPort != nil {
		t.Fatalf("expected nil host_port, got %v", *spec.HostPort)
	}
	if len(spec.Protocols) != 1 || spec.Protocols[0] != "tcp" {
		t.Fatalf("unexpected protocols: %#v", spec.Protocols)
	}
}

func TestParsePortSpec_HostAndBoth(t *testing.T) {
	spec, err := ParsePortSpec("18080:8080/both")
	if err != nil {
		t.Fatalf("ParsePortSpec returned error: %v", err)
	}
	if spec.HostPort == nil || *spec.HostPort != 18080 {
		t.Fatalf("expected host_port=18080, got %+v", spec.HostPort)
	}
	if spec.ContainerPort != 8080 {
		t.Fatalf("expected container_port=8080, got %d", spec.ContainerPort)
	}
	if len(spec.Protocols) != 2 {
		t.Fatalf("unexpected protocols len: %#v", spec.Protocols)
	}
}

func TestFormatPortSpec_DefaultTCP(t *testing.T) {
	got, err := FormatPortSpec(PortMappingSpec{
		ContainerPort: 8080,
		Protocols:     []string{"tcp"},
	})
	if err != nil {
		t.Fatalf("FormatPortSpec returned error: %v", err)
	}
	if got != "8080" {
		t.Fatalf("expected 8080, got %q", got)
	}
}

func TestFormatPortSpec_Both(t *testing.T) {
	host := 18080
	got, err := FormatPortSpec(PortMappingSpec{
		HostPort:      &host,
		ContainerPort: 8080,
		Protocols:     []string{"udp", "tcp"},
	})
	if err != nil {
		t.Fatalf("FormatPortSpec returned error: %v", err)
	}
	if got != "18080:8080/both" {
		t.Fatalf("expected 18080:8080/both, got %q", got)
	}
}
