// verify-update is the Windows installer's immutable, checksum-pinned bootstrap
// verifier. It does not enroll, install services, or execute downloaded code.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"

	protocol "github.com/antgpu/connector-protocol"
)

const publicKey = "cxFiE5lAcyYrklDmSVbdleWIVaCdpHi4p8HR7i0gO4k="

func verify(payload []byte, origin, key string) (map[string]string, error) {
	var manifest protocol.UpdateManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return nil, err
	}
	if err := protocol.VerifyUpdateManifest(manifest, key); err != nil {
		return nil, err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`).MatchString(manifest.Version) {
		return nil, fmt.Errorf("invalid version")
	}
	base, err := url.Parse(origin)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil {
		return nil, fmt.Errorf("invalid HTTPS manifest origin")
	}
	seen := map[string]bool{}
	var selected map[string]string
	for _, item := range manifest.Binaries {
		u, err := url.Parse(item.URL)
		if err != nil || u.Scheme != "https" || u.Host != base.Host || u.User != nil {
			return nil, fmt.Errorf("artifact origin differs from manifest origin")
		}
		if !regexp.MustCompile(`^[a-fA-F0-9]{64}$`).MatchString(item.SHA256) {
			return nil, fmt.Errorf("invalid artifact checksum")
		}
		platform := item.OS + "/" + item.Arch
		if seen[platform] {
			return nil, fmt.Errorf("duplicate platform")
		}
		seen[platform] = true
		if platform == "windows/amd64" {
			selected = map[string]string{"version": manifest.Version, "url": item.URL, "sha256": item.SHA256}
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("no signed windows/amd64 artifact")
	}
	return selected, nil
}

func main() {
	path := flag.String("manifest", "", "downloaded manifest path")
	origin := flag.String("origin", "", "HTTPS manifest URL")
	flag.Parse()
	payload, err := os.ReadFile(*path)
	if err == nil && len(payload) > 1024*1024 {
		err = fmt.Errorf("manifest exceeds size limit")
	}
	var selected map[string]string
	if err == nil {
		selected, err = verify(payload, *origin, publicKey)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(selected); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
