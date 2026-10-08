package docker

import (
	"fmt"
	"strings"

	protocol "github.com/antgpu/connector-protocol"
)

func reservedLabel(key string) bool {
	return key == "managed-by" || key == "template-name" || key == "onstart-script-path" || strings.HasPrefix(key, "antgpu.")
}

func ValidateTemplateLabels(t protocol.Template) error {
	_, _, err := templateLabels(t)
	return err
}

func templateLabels(t protocol.Template) (map[string]string, []string, error) {
	labels := make(map[string]string)
	for _, filter := range t.ExtraFilters {
		key, value, _ := strings.Cut(filter, "=")
		if reservedLabel(key) {
			return nil, nil, fmt.Errorf("template cannot set reserved label %q", key)
		}
		labels[key] = value
	}
	env, extra, err := parseDockerOptions(t.DockerOptions)
	if err != nil {
		return nil, nil, err
	}
	for key, value := range extra {
		if reservedLabel(key) {
			return nil, nil, fmt.Errorf("template cannot set reserved label %q", key)
		}
		labels[key] = value
	}
	return labels, env, nil
}
