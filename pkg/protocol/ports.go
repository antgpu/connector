package protocol

import (
	"fmt"
	"strconv"
	"strings"
)

type PortMappingSpec struct {
	HostPort      *int     `json:"host_port,omitempty"`
	ContainerPort int      `json:"container_port"`
	Protocols     []string `json:"protocols"`
}

func ParsePortSpec(raw string) (PortMappingSpec, error) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return PortMappingSpec{}, fmt.Errorf("port mapping cannot be empty")
	}

	portSpec := spec
	protocolSpec := ""
	if idx := strings.LastIndex(spec, "/"); idx >= 0 {
		portSpec = strings.TrimSpace(spec[:idx])
		protocolSpec = strings.ToLower(strings.TrimSpace(spec[idx+1:]))
	}

	protocols, err := parseProtocols(protocolSpec)
	if err != nil {
		return PortMappingSpec{}, err
	}

	parts := strings.Split(portSpec, ":")
	switch len(parts) {
	case 1:
		containerPort, err := parsePortNumber(parts[0])
		if err != nil {
			return PortMappingSpec{}, fmt.Errorf("invalid container port: %w", err)
		}
		return PortMappingSpec{
			ContainerPort: containerPort,
			Protocols:     protocols,
		}, nil
	case 2:
		hostPort, err := parsePortNumber(parts[0])
		if err != nil {
			return PortMappingSpec{}, fmt.Errorf("invalid host port: %w", err)
		}
		containerPort, err := parsePortNumber(parts[1])
		if err != nil {
			return PortMappingSpec{}, fmt.Errorf("invalid container port: %w", err)
		}
		return PortMappingSpec{
			HostPort:      &hostPort,
			ContainerPort: containerPort,
			Protocols:     protocols,
		}, nil
	default:
		return PortMappingSpec{}, fmt.Errorf("invalid format, expected container[/proto] or host:container[/proto]")
	}
}

func ParsePortSpecs(raw []string) ([]PortMappingSpec, error) {
	out := make([]PortMappingSpec, 0, len(raw))
	for _, item := range raw {
		spec, err := ParsePortSpec(item)
		if err != nil {
			return nil, fmt.Errorf("invalid mapping %q: %w", item, err)
		}
		out = append(out, spec)
	}
	return out, nil
}

func ParsePortSpecsBestEffort(raw []string) []PortMappingSpec {
	out := make([]PortMappingSpec, 0, len(raw))
	for _, item := range raw {
		spec, err := ParsePortSpec(item)
		if err != nil {
			continue
		}
		out = append(out, spec)
	}
	return out
}

func FormatPortSpec(spec PortMappingSpec) (string, error) {
	if spec.ContainerPort < 1 || spec.ContainerPort > 65535 {
		return "", fmt.Errorf("container_port must be in range 1..65535")
	}
	if spec.HostPort != nil {
		if *spec.HostPort < 1 || *spec.HostPort > 65535 {
			return "", fmt.Errorf("host_port must be in range 1..65535")
		}
	}

	protocols := normalizeProtocols(spec.Protocols)
	if len(protocols) == 0 {
		protocols = []string{"tcp"}
	}

	portPart := strconv.Itoa(spec.ContainerPort)
	if spec.HostPort != nil {
		portPart = fmt.Sprintf("%d:%d", *spec.HostPort, spec.ContainerPort)
	}

	suffix := ""
	switch {
	case len(protocols) == 2 && protocols[0] == "tcp" && protocols[1] == "udp":
		suffix = "/both"
	case len(protocols) == 1:
		if protocols[0] != "tcp" {
			suffix = "/" + protocols[0]
		}
	default:
		return "", fmt.Errorf("invalid protocols set")
	}

	return portPart + suffix, nil
}

func FormatPortSpecs(specs []PortMappingSpec) ([]string, error) {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		item, err := FormatPortSpec(spec)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func parseProtocols(raw string) ([]string, error) {
	switch raw {
	case "":
		return []string{"tcp"}, nil
	case "tcp":
		return []string{"tcp"}, nil
	case "udp":
		return []string{"udp"}, nil
	case "both":
		return []string{"tcp", "udp"}, nil
	default:
		return nil, fmt.Errorf("invalid protocol %q: allowed tcp, udp, both", raw)
	}
}

func normalizeProtocols(protocols []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 2)
	for _, protocol := range protocols {
		p := strings.ToLower(strings.TrimSpace(protocol))
		if p == "" {
			continue
		}
		if p != "tcp" && p != "udp" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 2 && out[0] == "udp" && out[1] == "tcp" {
		return []string{"tcp", "udp"}
	}
	return out
}

func parsePortNumber(raw string) (int, error) {
	port := strings.TrimSpace(raw)
	if port == "" {
		return 0, fmt.Errorf("empty")
	}
	v, err := strconv.Atoi(port)
	if err != nil {
		return 0, err
	}
	if v < 1 || v > 65535 {
		return 0, fmt.Errorf("out of range")
	}
	return v, nil
}
