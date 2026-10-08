package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/swaggest/usecase/status"
)

type ContainerSSHProbeInput struct {
	ContainerID string `path:"container_id"`
}

type ContainerSSHProbeOutput struct {
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
	ContainerIP string `json:"container_ip,omitempty"`
	Port        int    `json:"port,omitempty"`
}

func (s APIService) ContainerSSHProbe(ctx context.Context, input ContainerSSHProbeInput, output *ContainerSSHProbeOutput) error {
	if strings.TrimSpace(input.ContainerID) == "" {
		return status.Wrap(fmt.Errorf("container_id is required"), status.InvalidArgument)
	}

	result, err := s.dockerService().ProbeContainerSSH(ctx, input.ContainerID)
	if err != nil {
		return wrapDockerErr("probe ssh", err)
	}

	output.Available = result.Available
	output.Reason = result.Reason
	output.ContainerIP = result.ContainerIP
	output.Port = result.Port
	return nil
}
