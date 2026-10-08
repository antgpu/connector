package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/antgpu/connector/pkg/protocol"
)

func TestTemplateCannotOverrideConnectorLabels(t *testing.T) {
	for _, key := range []string{"onstart-script-path", "managed-by", "template-name", "antgpu.image_owned", "antgpu.image_ref", "antgpu.rent_id", "antgpu.storage_safety_reserve_bytes"} {
		for _, useOptions := range []bool{false, true} {
			template := protocol.Template{ImagePath: "invalid.example/image"}
			if useOptions {
				template.DockerOptions = "--label " + key + "=attacker"
			} else {
				template.ExtraFilters = []string{key + "=attacker"}
			}
			// This must reject before even contacting Docker or pulling an image.
			_, err := LaunchContainer(context.Background(), template)
			if err == nil || !strings.Contains(err.Error(), "reserved label") {
				t.Errorf("key=%s options=%v: %v", key, useOptions, err)
			}
		}
	}
	if err := ValidateTemplateLabels(protocol.Template{ExtraFilters: []string{"ai.antgpu.runtime=llama.cpp", "customer.label=allowed"}}); err != nil {
		t.Fatal(err)
	}
}
