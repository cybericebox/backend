package imageRegistryModel

import "testing"

func TestPinnedReference(t *testing.T) {
	digest := "sha256:" + hexA
	cases := []struct{ in, want string }{
		{"cybericebox/exercise-web-x:v1.0.2", "cybericebox/exercise-web-x:v1.0.2@" + digest},
		{"nginx", "nginx:latest@" + digest},
		{"ghcr.io/acme/app", "ghcr.io/acme/app:latest@" + digest},
		{"localhost:5000/app", "localhost:5000/app:latest@" + digest},
		{"localhost:5000/app:2", "localhost:5000/app:2@" + digest},
	}
	for _, c := range cases {
		if got := PinnedReference(c.in, digest); got != c.want {
			t.Errorf("PinnedReference(%q) = %q, want %q", c.in, got, c.want)
		}
		if ref, err := ParseImageRef(c.want); err != nil || ref.Digest != digest {
			t.Errorf("%q does not parse back with its digest: %+v %v", c.want, ref, err)
		}
	}
}

func TestHasDigest(t *testing.T) {
	digest := "sha256:" + hexA
	for in, want := range map[string]bool{
		"nginx":                  false,
		"nginx:1.2":              false,
		"nginx:1.2@" + digest:    true,
		"nginx@" + digest:        true,
		"platform:web@" + digest: false, // a platform reference is its own pinned form, not an external one
		"":                       false,
		"nginx@sha256:abc":       false,
	} {
		if got := HasDigest(in); got != want {
			t.Errorf("HasDigest(%q) = %v, want %v", in, got, want)
		}
	}
}
