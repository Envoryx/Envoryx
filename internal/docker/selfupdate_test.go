package docker

import "testing"

func TestRepoDigest(t *testing.T) {
	const sum = "sha256:4ffadc0002b2581327e06101fc8c06cd189232baf79fe561fac9caeb76f5e807"
	digests := []string{"mirror.example/envoryx/envoryx@sha256:" + "ab" + sum[9:], "ghcr.io/envoryx/envoryx@" + sum}
	if got, err := repoDigest("ghcr.io/envoryx/envoryx", digests); err != nil || got != sum {
		t.Fatalf("repoDigest = %q, %v", got, err)
	}
	if _, err := repoDigest("ghcr.io/envoryx/envoryx", nil); err == nil {
		t.Error("an image without a digest from its registry passed")
	}
	if _, err := repoDigest("ghcr.io/envoryx/envoryx", digests[:1]); err == nil {
		t.Error("the digest of another repository passed")
	}
}
