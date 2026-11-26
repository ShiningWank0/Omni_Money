//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package secretfile

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

// Every nine-bit mode for a regular one-byte file is checked against the
// rights that each class of user is allowed to have. No host files are opened.
func TestAllUnixSecretPermissionModes(t *testing.T) {
	type rights struct{ owner, group, other fs.FileMode }
	readOnly := func(digit fs.FileMode) bool { return digit == 0 || digit == 4 }
	ownerData := func(digit fs.FileMode) bool { return readOnly(digit) || digit == 2 || digit == 6 }

	private := func(r rights) bool { return ownerData(r.owner) && r.group == 0 && r.other == 0 }
	cases := []struct {
		name   string
		path   string
		policy policy
		allow  func(rights) bool
	}{
		{"integrity", "/etc/omni/config", integrityProtected, func(r rights) bool {
			return ownerData(r.owner) && readOnly(r.group) && readOnly(r.other)
		}},
		{"host", "/etc/omni/secret", confidential, private},
		{"compose", "/run/secrets/key", confidential, func(r rights) bool {
			return readOnly(r.owner) && readOnly(r.group) && readOnly(r.other)
		}},
		{"nested", "/run/secrets/nested/key", confidential, private},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for mode := fs.FileMode(0); mode <= fs.ModePerm; mode++ {
				info, err := fs.Stat(fstest.MapFS{"key": {Data: []byte{'x'}, Mode: mode}}, "key")
				if err != nil {
					t.Fatal(err)
				}
				r := rights{mode >> 6, (mode >> 3) & 7, mode & 7}
				want := tc.allow(r)
				if err := validateFileInfo(tc.path, info, 1, tc.policy); (err == nil) != want {
					t.Fatalf("mode %03o: policy expects accepted=%t, validation returned %v", mode, want, err)
				}
			}
		})
	}
}
