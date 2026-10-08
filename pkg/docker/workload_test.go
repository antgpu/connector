package docker

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"strings"
)

func containerCommand(ctx context.Context, cli *client.Client, id string, command []string) (string, error) {
	exec, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{Cmd: command, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", err
	}
	stream, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", err
	}
	defer stream.Close()
	var output strings.Builder
	_, _ = stdcopy.StdCopy(&output, &output, stream.Reader)
	info, err := cli.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return output.String(), err
	}
	if info.ExitCode != 0 {
		return output.String(), fmt.Errorf("exit code %d", info.ExitCode)
	}
	return output.String(), nil
}
