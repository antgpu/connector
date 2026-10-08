package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	backendDocker "github.com/antgpu/connector/pkg/protocol"
)

func TestFetchTemplateByID_ForwardsUserIDHeader(t *testing.T) {
	var gotAuth string
	var gotUserID string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUserID = r.Header.Get("X-User-ID")
		if r.URL.Path != "/templates/12" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":12,"template_name":"private","image_path":"img"}`))
	}))
	defer ts.Close()

	s := APIService{
		APIAddr: ts.URL,
		MachineAuthData: MachineAuthData{
			MachineToken: "machine-token",
		},
	}
	userID := 44
	if _, err := s.fetchTemplateByID(context.Background(), 12, &userID); err != nil {
		t.Fatalf("fetchTemplateByID error: %v", err)
	}
	if gotAuth != "Bearer machine-token" {
		t.Fatalf("unexpected auth header: %q", gotAuth)
	}
	if gotUserID != "44" {
		t.Fatalf("expected X-User-ID=44, got %q", gotUserID)
	}
}

func TestResolveContainerTemplate_UsesControlPlanePayload(t *testing.T) {
	s := APIService{APIAddr: "://invalid"}
	resolved := &backendDocker.Template{
		ID:           12,
		TemplateName: "private",
		ImagePath:    "registry.example/private:1",
		IsPrivate:    true,
	}

	got, err := s.resolveContainerTemplate(context.Background(), 12, resolved, nil)
	if err != nil {
		t.Fatalf("resolveContainerTemplate error: %v", err)
	}
	if got == resolved {
		t.Fatal("expected a copy of the control-plane template")
	}
	if got.ID != 12 || got.TemplateName != "private" || !got.IsPrivate {
		t.Fatalf("unexpected resolved template: %+v", got)
	}
}
