package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/antgpu/connector/internal/securefs"
	"github.com/docker/docker/client"
)

var ownershipDir = filepath.Join("cache", "containers")
var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type containerOwnership struct {
	ImageID        string `json:"image_id"`
	ImageRef       string `json:"image_ref"`
	ImageOwned     bool   `json:"image_owned"`
	StorageReserve uint64 `json:"storage_reserve"`
	ScriptPath     string `json:"script_path"`
}

func ownershipPath(id string) (string, error) {
	if !containerIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid full container ID")
	}
	return filepath.Join(ownershipDir, id+".json"), nil
}

func recordContainerOwnership(ctx context.Context, cli *client.Client, id, imageRef string, owned bool, script string, reserve uint64) error {
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return err
	}
	record := containerOwnership{ImageID: info.Image, ImageRef: imageRef, ImageOwned: owned, ScriptPath: script, StorageReserve: reserve}
	path, err := ownershipPath(id)
	if err != nil {
		return err
	}
	if err := securefs.PrivateDir(ownershipDir); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		os.Remove(path)
		return err
	}
	return closeErr
}

func loadContainerOwnership(id string) (containerOwnership, error) {
	var record containerOwnership
	path, err := ownershipPath(id)
	if err != nil {
		return record, err
	}
	if err := securefs.RegularFile(path); err != nil {
		return record, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	err = json.Unmarshal(data, &record)
	return record, err
}
