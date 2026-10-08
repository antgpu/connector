package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/swaggest/usecase/status"
)

type TunnelPortMappingsApplyInput struct {
	PortMappings []string `json:"port_mappings"`
}

type TunnelPortMappingsApplyOutput struct {
	PortMappings []string `json:"port_mappings"`
}

func (s APIService) TunnelPortMappingsApply(ctx context.Context, input TunnelPortMappingsApplyInput, output *TunnelPortMappingsApplyOutput) error {
	_ = ctx

	if s.TunnelRuntime == nil {
		return status.Wrap(fmt.Errorf("tunnel runtime is not configured"), status.FailedPrecondition)
	}

	applied, err := s.TunnelRuntime.ApplyDynamicPortMappings(input.PortMappings)
	if err != nil {
		if strings.Contains(err.Error(), "invalid port mapping format") ||
			strings.Contains(err.Error(), "invalid port number") ||
			strings.Contains(err.Error(), "invalid protocol") {
			return status.Wrap(fmt.Errorf("apply tunnel mappings: %w", err), status.InvalidArgument)
		}
		return status.Wrap(fmt.Errorf("apply tunnel mappings: %w", err), status.Internal)
	}

	output.PortMappings = applied
	return nil
}
