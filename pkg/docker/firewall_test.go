package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFirewallDoesNotAttachPartialPolicy(t *testing.T) {
	for _, fail := range []string{"-S DOCKER-USER", "-A ANTGPU-EGRESS -d 169.254.0.0/16 -j REJECT"} {
		t.Run(fail, func(t *testing.T) {
			hooks := 0
			run := func(_ context.Context, args ...string) error {
				command := strings.Join(args, " ")
				if command == fail || args[0] == "-C" {
					return errors.New("injected firewall failure")
				}
				if args[0] == "-I" {
					hooks++
				}
				return nil
			}
			if err := configureWorkloadFirewall(context.Background(), run); err == nil {
				t.Fatal("partial policy accepted")
			}
			if hooks != 0 {
				t.Fatal("partial policy attached before completing restrictions")
			}
		})
	}
}
