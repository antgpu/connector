package api

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	internaldocker "github.com/antgpu/connector/internal/docker"
	pkgdocker "github.com/antgpu/connector/pkg/docker"
	backendDocker "github.com/antgpu/connector/pkg/protocol"
	"github.com/google/uuid"
	"github.com/swaggest/usecase/status"
)

type ProvisioningOperation struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	Message         string `json:"message,omitempty"`
	ContainerID     string `json:"container_id,omitempty"`
	PullCurrent     int64  `json:"pull_current_bytes,omitempty"`
	PullTotal       int64  `json:"pull_total_bytes,omitempty"`
	PullLayer       string `json:"pull_layer,omitempty"`
	PullLayerIndex  int    `json:"pull_layer_index,omitempty"`
	PullLayersTotal int    `json:"pull_layers_total,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type ProvisioningManager struct {
	mu      sync.RWMutex
	ops     map[string]ProvisioningOperation
	cancels map[string]context.CancelFunc
	layers  map[string]*provisioningLayers
}

type provisioningLayers struct {
	indexes map[string]int
}

func NewProvisioningManager() *ProvisioningManager {
	return &ProvisioningManager{
		ops:     make(map[string]ProvisioningOperation),
		cancels: make(map[string]context.CancelFunc),
		layers:  make(map[string]*provisioningLayers),
	}
}

func (m *ProvisioningManager) registerCancel(id string, cancel context.CancelFunc) {
	m.mu.Lock()
	m.cancels[id] = cancel
	m.mu.Unlock()
}

func (m *ProvisioningManager) cancel(id string) (ProvisioningOperation, bool) {
	m.mu.Lock()
	op, ok := m.ops[id]
	if !ok || op.Status == "ready" || op.Status == "failed" || op.Status == "canceled" {
		m.mu.Unlock()
		return op, false
	}
	cancel := m.cancels[id]
	delete(m.cancels, id)
	op.Status, op.Message, op.UpdatedAt = "canceled", "provisioning canceled", time.Now().UTC().Format(time.RFC3339)
	m.ops[id] = op
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return op, true
}

func (m *ProvisioningManager) put(op ProvisioningOperation) {
	m.mu.Lock()
	m.ops[op.ID] = op
	m.mu.Unlock()
}

func (m *ProvisioningManager) updatePull(id string, progress internaldocker.PullProgress) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[id]
	if !ok || op.Status == "canceled" {
		return
	}
	if progress.LayerID != "" {
		layers := m.layers[id]
		if layers == nil {
			layers = &provisioningLayers{indexes: make(map[string]int)}
			m.layers[id] = layers
		}
		if _, exists := layers.indexes[progress.LayerID]; !exists {
			layers.indexes[progress.LayerID] = len(layers.indexes) + 1
		}
		op.PullLayer = progress.LayerID
		op.PullLayerIndex = layers.indexes[progress.LayerID]
		op.PullLayersTotal = len(layers.indexes)
	}
	op.Status = "pulling"
	op.Message = progress.Status
	op.PullCurrent = progress.Current
	op.PullTotal = progress.Total
	op.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	m.ops[id] = op
}
func (m *ProvisioningManager) get(id string) (ProvisioningOperation, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	op, ok := m.ops[id]
	return op, ok
}

type ContainerProvisioningInput struct {
	TemplateID                int                     `json:"template_id"`
	RentID                    string                  `json:"rent_id,omitempty"`
	StorageSafetyReserveBytes uint64                  `json:"storage_safety_reserve_bytes,omitempty"`
	Template                  *backendDocker.Template `json:"template,omitempty"`
	CurrentUserID             *int                    `header:"X-User-ID"`
}
type ContainerProvisioningOutput struct {
	Operation ProvisioningOperation `json:"operation"`
}
type ContainerProvisioningStatusInput struct {
	OperationID string `path:"operation_id"`
}

func (s APIService) ContainerCreateFromTemplateAsync(ctx context.Context, input ContainerProvisioningInput, output *ContainerProvisioningOutput) error {
	if input.TemplateID <= 0 {
		return status.Wrap(fmt.Errorf("template_id is required"), status.InvalidArgument)
	}
	template, err := s.resolveContainerTemplate(ctx, input.TemplateID, input.Template, input.CurrentUserID)
	if err != nil {
		return err
	}
	manager := s.Provisioning
	if manager == nil {
		return status.Wrap(fmt.Errorf("provisioning manager unavailable"), status.Internal)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	op := ProvisioningOperation{ID: uuid.NewString(), Status: "queued", Message: "queued for image pull", CreatedAt: now, UpdatedAt: now}
	manager.put(op)
	pullCtx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	manager.registerCancel(op.ID, cancel)
	output.Operation = op
	go func() {
		defer cancel()
		if current, ok := manager.get(op.ID); ok && current.Status == "canceled" {
			return
		}
		op.Status, op.Message, op.UpdatedAt = "pulling", "pulling image", time.Now().UTC().Format(time.RFC3339)
		manager.put(op)
		result, runErr := s.dockerService().CreateFromTemplateWithOptions(pullCtx, *template, func(progress internaldocker.PullProgress) {
			manager.updatePull(op.ID, progress)
		}, pkgdocker.LaunchOptions{RentID: strings.TrimSpace(input.RentID), StorageSafetyReserveBytes: input.StorageSafetyReserveBytes})
		if current, ok := manager.get(op.ID); ok && current.Status == "canceled" {
			return
		}
		op.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if runErr != nil {
			op.Status, op.Message = "failed", runErr.Error()
		} else {
			op.Status, op.Message, op.ContainerID = "ready", "container started", result.ContainerID
			s.refreshSpecsAsync("container_provisioned")
		}
		manager.put(op)
	}()
	return nil
}

func (s APIService) ContainerCancelProvisioning(_ context.Context, input ContainerProvisioningStatusInput, output *ContainerProvisioningOutput) error {
	if s.Provisioning == nil {
		return status.Wrap(fmt.Errorf("provisioning manager unavailable"), status.NotFound)
	}
	op, canceled := s.Provisioning.cancel(input.OperationID)
	if !canceled {
		return status.Wrap(fmt.Errorf("provisioning operation cannot be canceled"), status.FailedPrecondition)
	}
	output.Operation = op
	return nil
}

func (s APIService) ContainerProvisioningStatus(_ context.Context, input ContainerProvisioningStatusInput, output *ContainerProvisioningOutput) error {
	if s.Provisioning == nil {
		return status.Wrap(fmt.Errorf("provisioning manager unavailable"), status.NotFound)
	}
	op, ok := s.Provisioning.get(input.OperationID)
	if !ok {
		return status.Wrap(fmt.Errorf("provisioning operation not found"), status.NotFound)
	}
	output.Operation = op
	return nil
}
