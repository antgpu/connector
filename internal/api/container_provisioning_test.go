package api

import "testing"

func TestCancelledProvisioningCannotBeResurrectedByProgress(t *testing.T) {
	m := NewProvisioningManager()
	m.put(ProvisioningOperation{ID: "fixture", Status: "pulling"})
	op, ok := m.cancel("fixture")
	if !ok || op.Finished {
		t.Fatal("cancel must not claim the worker finished")
	}
	m.put(ProvisioningOperation{ID: "fixture", Status: "pulling", Message: "late progress"})
	current, _ := m.get("fixture")
	if current.Status != "canceled" || current.Finished {
		t.Fatal("late progress resurrected canceled provisioning")
	}
	// A container may finish creating while the cancellation is being processed.
	// Its identity must remain available for backend cleanup before power-off.
	m.put(ProvisioningOperation{ID: "fixture", Status: "ready", ContainerID: "fixture-container", Finished: true})
	current, _ = m.get("fixture")
	if current.Status != "canceled" || !current.Finished || current.ContainerID != "fixture-container" {
		t.Fatalf("lost cancellation completion or cleanup identity: %+v", current)
	}
}
