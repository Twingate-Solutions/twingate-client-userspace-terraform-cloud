package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"testing"

	"github.com/ulikunitz/xz"
)

// DebFile is one entry in a test .deb.
type DebFile struct {
	Name     string
	Body     string
	Mode     int64
	Linkname string // set for a symlink
}

// BuildDeb returns a minimal .deb whose data member uses compression ext
// ("", ".gz" or ".xz").
func BuildDeb(t testing.TB, ext string, files []DebFile) []byte {
	t.Helper()
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	for _, f := range files {
		h := &tar.Header{Name: f.Name, Mode: f.Mode, Size: int64(len(f.Body)), Typeflag: tar.TypeReg}
		if f.Linkname != "" {
			h = &tar.Header{Name: f.Name, Linkname: f.Linkname, Typeflag: tar.TypeSymlink}
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if f.Linkname == "" {
			tw.Write([]byte(f.Body))
		}
	}
	tw.Close()

	var data bytes.Buffer
	switch ext {
	case ".gz":
		zw := gzip.NewWriter(&data)
		zw.Write(tb.Bytes())
		zw.Close()
	case ".xz":
		zw, err := xz.NewWriter(&data)
		if err != nil {
			t.Fatal(err)
		}
		zw.Write(tb.Bytes())
		zw.Close()
	default:
		data = tb
	}

	var ar bytes.Buffer
	ar.WriteString("!<arch>\n")
	member := func(name string, body []byte) {
		fmt.Fprintf(&ar, "%-16s%-12s%-6s%-6s%-8s%-10d`\n", name, "0", "0", "0", "100644", len(body))
		ar.Write(body)
		if len(body)%2 == 1 {
			ar.WriteByte('\n')
		}
	}
	member("debian-binary", []byte("2.0\n"))
	member("control.tar.gz", []byte("odd"))
	member("data.tar"+ext, data.Bytes())
	return ar.Bytes()
}
