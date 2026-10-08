package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	internaldocker "github.com/antgpu/connector/internal/docker"
	"golang.org/x/net/websocket"
)

func TestMachineTokenAuthMiddleware(t *testing.T) {
	svc := APIService{MachineAuthData: MachineAuthData{MachineToken: "secret"}}
	handler := svc.machineTokenAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for name, tc := range map[string]struct {
		token string
		want  int
	}{
		"missing": {want: http.StatusUnauthorized},
		"wrong":   {token: "not-secret", want: http.StatusUnauthorized},
		"valid":   {token: "secret", want: http.StatusNoContent},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/connector/status", nil)
			req.Header.Set("X-Machine-Token", tc.token)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, rr.Code)
			}
		})
	}
}

func TestMachineTokenAuthMiddleware_AllowsOnlyModelRunnerInferenceWithoutToken(t *testing.T) {
	svc := APIService{MachineAuthData: MachineAuthData{MachineToken: "secret"}}
	handler := svc.machineTokenAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for name, tc := range map[string]struct {
		path string
		want int
	}{
		"engines root":       {path: "/engines", want: http.StatusNoContent},
		"inference endpoint": {path: "/engines/v1/chat/completions", want: http.StatusNoContent},
		"lookalike":          {path: "/engines-admin", want: http.StatusUnauthorized},
		"runner status":      {path: "/model-runner/status", want: http.StatusUnauthorized},
		"runner configure":   {path: "/configure/model", want: http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("path %s: expected %d, got %d", tc.path, tc.want, rr.Code)
			}
		})
	}
}

func TestMachineTokenAuthMiddlewareRejectsUnconfiguredToken(t *testing.T) {
	handler := (APIService{}).machineTokenAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/connector/status", nil)
	req.Header.Set("X-Machine-Token", "anything")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestRejectBrowserWebSocketOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/containers/c1/logs/ws", nil)
	if err := rejectBrowserWebSocketOrigin(&websocket.Config{}, req); err != nil {
		t.Fatalf("unexpected server-origin error: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example")
	if err := rejectBrowserWebSocketOrigin(&websocket.Config{}, req); err == nil {
		t.Fatal("expected browser origin to be rejected")
	}
}

func TestWebServiceProtectsHTTPAndWebSocketRoutes(t *testing.T) {
	svc := APIService{
		MachineAuthData: MachineAuthData{MachineToken: "secret"},
		DockerService:   &internaldocker.Service{},
	}
	handler := svc.WebService()
	for _, path := range []string{"/connector/status", "/containers/c1/logs/ws", "/containers/c1/runtime/capabilities", "/runtime/dmr/api/models/create"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("path %s: expected %d, got %d", path, http.StatusUnauthorized, rr.Code)
		}
	}
}
