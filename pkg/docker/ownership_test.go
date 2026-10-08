package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antgpu/connector/internal/securefs"
	"github.com/docker/docker/client"
)

func TestContainerCleanupRequiresPrivateOwnershipReceipt(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if err := securefs.PrivateDir(resolveTemplateScriptsDir()); err != nil {
		t.Fatal(err)
	}
	victim, err := filepath.Abs(filepath.Join(resolveTemplateScriptsDir(), "victim.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte("preserve"), 0o700); err != nil {
		t.Fatal(err)
	}
	const imageID = "sha256:owned-image"
	id := strings.Repeat("a", 64)
	imageDeletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Image": imageID, "Config": map[string]any{"Labels": map[string]string{"managed-by": "template-manager", "onstart-script-path": victim, "antgpu.image_owned": "true", "antgpu.image_ref": "provider-critical-image", "antgpu.storage_safety_reserve_bytes": "18446744073709551615"}}})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/info"):
			fmt.Fprint(w, `{"DockerRootDir":""}`)
		case strings.Contains(r.URL.Path, "/images/"):
			imageDeletes++
			t.Error("labels triggered image deletion")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if err := RemoveContainer(context.Background(), cli, id, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("forged label deleted provider file: %v", err)
	}
	if imageDeletes != 0 {
		t.Fatal("forged image ownership accepted")
	}
	// Even a path inside the scripts directory is only descriptive until an
	// independently stored receipt records that this connector created it.
	if err := securefs.PrivateDir(ownershipDir); err != nil {
		t.Fatal(err)
	}
	path, err := ownershipPath(id)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(containerOwnership{ImageID: imageID, ScriptPath: victim})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveContainer(context.Background(), cli, id, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); !os.IsNotExist(err) {
		t.Fatalf("owned script not cleaned: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("ownership receipt not cleaned: %v", err)
	}
}
