package docker

import "testing"

func TestMountInfoHasProjectQuota(t *testing.T) {
	raw := `36 25 0:31 / / rw,relatime - ext4 /dev/root rw
42 36 0:44 / /var/lib/docker rw,relatime - xfs /dev/nvme0n1p2 rw,attr2,inode64,prjquota`
	hasQuota, err := mountInfoHasProjectQuota(raw, "/var/lib/docker/overlay2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasQuota {
		t.Fatal("expected project quota to be detected")
	}
}

func TestMountInfoHasProjectQuotaRejectsUnverifiedMount(t *testing.T) {
	raw := `36 25 0:31 / / rw,relatime - xfs /dev/root rw,attr2,inode64`
	hasQuota, err := mountInfoHasProjectQuota(raw, "/var/lib/docker")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasQuota {
		t.Fatal("expected xfs without pquota/prjquota to remain soft-only")
	}
}

func TestDecodeMountInfoPath(t *testing.T) {
	if got := decodeMountInfoPath(`/mnt/docker\040data`); got != "/mnt/docker data" {
		t.Fatalf("unexpected decoded path %q", got)
	}
}

func TestManagedImageEvictionCandidate(t *testing.T) {
	labels := map[string]string{
		"antgpu.image_owned":                  "true",
		"antgpu.image_ref":                    "registry.example/antgpu/workload:1",
		"antgpu.storage_safety_reserve_bytes": "100",
	}
	if ref, ok := managedImageEvictionCandidate(labels, 99); !ok || ref != labels["antgpu.image_ref"] {
		t.Fatalf("expected owned image below reserve, got ref=%q ok=%v", ref, ok)
	}
	if _, ok := managedImageEvictionCandidate(labels, 101); ok {
		t.Fatal("must not evict above the safety reserve")
	}
	labels["antgpu.image_owned"] = "false"
	if _, ok := managedImageEvictionCandidate(labels, 1); ok {
		t.Fatal("must not evict an image that existed before AntGPU pulled it")
	}
}
