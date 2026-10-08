package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConnectorRemove_RejectsMachineIDMismatch(t *testing.T) {
	svc := APIService{
		MachineAuthData:   MachineAuthData{MachineID: "local-machine"},
		OnConnectorRemove: func(ConnectorRemoveInput) {},
	}

	out := ConnectorRemoveOutput{}
	err := svc.ConnectorRemove(context.Background(), ConnectorRemoveInput{
		MachineID: "other-machine",
		Reason:    "removed_from_platform",
	}, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "machine_id mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConnectorRemove_InvokesHandler(t *testing.T) {
	called := make(chan ConnectorRemoveInput, 1)
	svc := APIService{
		MachineAuthData: MachineAuthData{MachineID: "local-machine"},
		OnConnectorRemove: func(in ConnectorRemoveInput) {
			called <- in
		},
	}

	out := ConnectorRemoveOutput{}
	err := svc.ConnectorRemove(context.Background(), ConnectorRemoveInput{
		MachineID:         "local-machine",
		Reason:            "removed_from_platform",
		RequestedByUserID: 42,
	}, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Accepted {
		t.Fatal("expected accepted output")
	}

	select {
	case in := <-called:
		if in.MachineID != "local-machine" {
			t.Fatalf("unexpected machine id: %s", in.MachineID)
		}
		if in.RequestedByUserID != 42 {
			t.Fatalf("unexpected requested_by_user_id: %d", in.RequestedByUserID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for remove handler callback")
	}
}
