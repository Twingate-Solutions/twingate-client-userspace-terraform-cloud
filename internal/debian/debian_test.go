package debian

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/testutil"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2026.239.6882", "2026.239.6882", 0},
		{"2026.239.6882", "2026.22.182963", 1},
		{"0.13.0", "2026.22.182963", -1},
		{"1.0~rc1", "1.0", -1},
		{"1:0.1", "2.0", 1},
		{"1.0-1", "1.0-2", -1},
		{"1.0a", "1.0", 1},
		{"1.0", "1.0.1", -1},
		{"2:2.2.2-3ubuntu2", "2:2.2.2-3", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := CompareVersions(c.b, c.a); got != -c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

const index = `Architecture: amd64
Depends: libdbus-1-3 (>=1.10),
 libnl-3-200
Package: twingate
Version: 2026.22.182963
SHA256: AAAA
Filename: files/a/twingate-amd64.deb

Package: twingate
Architecture: amd64
Version: 2026.239.6882
SHA256: bbbb
Filename: files/b/twingate-amd64.deb

Package: twingate
Architecture: arm64
Version: 2026.300.1
Filename: files/c/twingate-arm64.deb

Package: twingate-connector
Architecture: amd64
Version: 9999.1
`

func TestParseAndSelect(t *testing.T) {
	st, err := ParsePackages(strings.NewReader(index))
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 4 {
		t.Fatalf("got %d stanzas, want 4", len(st))
	}
	if got := st[0]["Depends"]; got != "libdbus-1-3 (>=1.10),\nlibnl-3-200" {
		t.Errorf("continuation not folded: %q", got)
	}
	if st[0].SHA256() != "aaaa" {
		t.Errorf("SHA256 not lowercased: %q", st[0].SHA256())
	}

	if s := Select(st, "twingate", "amd64", ""); s == nil || s.Version() != "2026.239.6882" {
		t.Errorf("latest amd64: got %v", s)
	}
	if s := Select(st, "twingate", "amd64", "2026.22.182963"); s == nil || s.Filename() != "files/a/twingate-amd64.deb" {
		t.Errorf("pinned: got %v", s)
	}
	if s := Select(st, "twingate", "amd64", "1.2.3"); s != nil {
		t.Errorf("missing version: got %v", s)
	}
}

func TestWalkData(t *testing.T) {
	for _, ext := range []string{".gz", ".xz", ""} {
		t.Run("data.tar"+ext, func(t *testing.T) {
			deb := testutil.BuildDeb(t, ext, []testutil.DebFile{
				{Name: "./usr/sbin/twingated", Body: "ELF-fake", Mode: 0o755},
				{Name: "./usr/lib/libx.so.1", Linkname: "libx.so.1.2"},
			})
			var got string
			var sawLink bool
			err := WalkData(bytes.NewReader(deb), func(h *tar.Header, r io.Reader) error {
				switch CleanPath(h.Name) {
				case "usr/sbin/twingated":
					b, _ := io.ReadAll(r)
					got = string(b)
				case "usr/lib/libx.so.1":
					sawLink = h.Typeflag == tar.TypeSymlink
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != "ELF-fake" || !sawLink {
				t.Errorf("got %q, symlink=%v", got, sawLink)
			}
		})
	}
}

func TestWalkDataRejectsNonDeb(t *testing.T) {
	if err := WalkData(strings.NewReader("not an archive"), func(*tar.Header, io.Reader) error { return nil }); err == nil {
		t.Fatal("expected error")
	}
}
