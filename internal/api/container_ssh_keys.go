package api

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/swaggest/usecase/status"
)

type ContainerSSHKeysApplyInput struct {
	ContainerID string   `path:"container_id"`
	PublicKeys  []string `json:"public_keys"`
}

type ContainerSSHKeysApplyOutput struct {
	Success bool `json:"success"`
}

func (s APIService) ContainerSSHKeysApply(ctx context.Context, input ContainerSSHKeysApplyInput, output *ContainerSSHKeysApplyOutput) error {
	if strings.TrimSpace(input.ContainerID) == "" {
		return status.Wrap(fmt.Errorf("container_id is required"), status.InvalidArgument)
	}
	if len(input.PublicKeys) == 0 {
		return status.Wrap(fmt.Errorf("public_keys must contain at least one key"), status.InvalidArgument)
	}

	if err := s.dockerService().ApplyContainerSSHPublicKeys(ctx, input.ContainerID, input.PublicKeys); err != nil {
		log.Printf("ssh keys: apply failed container=%s: %v", strings.TrimSpace(input.ContainerID), err)
		if strings.Contains(err.Error(), "public_keys must contain at least one key") {
			return status.Wrap(fmt.Errorf("apply ssh keys: %w", err), status.InvalidArgument)
		}
		if strings.Contains(err.Error(), "does not provide sshd") {
			return status.Wrap(fmt.Errorf("apply ssh keys: %w", err), status.FailedPrecondition)
		}
		return wrapDockerErr("apply ssh keys", err)
	}

	output.Success = true
	return nil
}
