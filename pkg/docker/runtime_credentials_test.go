package docker

import (
	"strings"
	"testing"
)

func TestRuntimeBootstrapCredentials(t *testing.T) {
	labels := map[string]string{"ai.antgpu.runtime": "unsloth", "ai.antgpu.runtime.bootstrap": "unsloth-studio"}
	first, err := provisionRuntimeCredentials(labels, []string{"VISIBLE=yes"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := provisionRuntimeCredentials(labels, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 4 || first[0] != "VISIBLE=yes" || first[1] != "ANTGPU_UNSLOTH_USERNAME=unsloth" {
		t.Fatal("unexpected bootstrap environment")
	}
	password := strings.TrimPrefix(first[2], "ANTGPU_UNSLOTH_PASSWORD=")
	if len(password) != 43 || first[3] != "UNSLOTH_STUDIO_PASSWORD="+password || first[2] == second[1] {
		t.Fatal("credentials must agree within a container and differ across containers")
	}
	for _, key := range []string{"ANTGPU_UNSLOTH_PASSWORD", "ANTGPU_UNSLOTH_USERNAME", "ANTGPU_RUNTIME_TOKEN", "UNSLOTH_STUDIO_PASSWORD"} {
		if _, err := provisionRuntimeCredentials(labels, []string{key + "=bad"}); err == nil {
			t.Fatalf("accepted conflicting %s", key)
		}
	}
	labels["ai.antgpu.runtime"] = "openai"
	if _, err := provisionRuntimeCredentials(labels, nil); err == nil {
		t.Fatal("accepted wrong runtime")
	}
}
