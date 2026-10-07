package debian

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

const arMagic = "!<arch>\n"

// WalkData calls fn for every entry of the data.tar.* member of the .deb read
// from r. Supported compressions: none, gzip, xz, zstd. Returning io.EOF from
// fn stops the walk early without error.
func WalkData(r io.Reader, fn func(hdr *tar.Header, body io.Reader) error) error {
	br := bufio.NewReader(r)
	magic := make([]byte, len(arMagic))
	if _, err := io.ReadFull(br, magic); err != nil {
		return fmt.Errorf("read ar magic: %w", err)
	}
	if string(magic) != arMagic {
		return errors.New("not a .deb (ar) archive")
	}

	hdr := make([]byte, 60)
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("data.tar member not found in .deb")
			}
			return fmt.Errorf("read ar header: %w", err)
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(hdr[0:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("bad ar member size for %q", name)
		}
		body := io.LimitReader(br, size)

		if strings.HasPrefix(name, "data.tar") {
			return walkTar(name, body, fn)
		}
		if _, err := io.Copy(io.Discard, body); err != nil {
			return fmt.Errorf("skip ar member %q: %w", name, err)
		}
		if size%2 == 1 {
			if _, err := br.Discard(1); err != nil {
				return fmt.Errorf("skip ar padding: %w", err)
			}
		}
	}
}

func walkTar(name string, r io.Reader, fn func(*tar.Header, io.Reader) error) error {
	var dr io.Reader
	switch {
	case name == "data.tar":
		dr = r
	case strings.HasSuffix(name, ".gz"):
		gz, err := gzip.NewReader(r)
		if err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
		defer func() { _ = gz.Close() }()
		dr = gz
	case strings.HasSuffix(name, ".xz"):
		x, err := xz.NewReader(r)
		if err != nil {
			return fmt.Errorf("xz: %w", err)
		}
		dr = x
	case strings.HasSuffix(name, ".zst"):
		z, err := zstd.NewReader(r)
		if err != nil {
			return fmt.Errorf("zstd: %w", err)
		}
		defer z.Close()
		dr = z
	default:
		return fmt.Errorf("unsupported data member compression: %s", name)
	}

	tr := tar.NewReader(dr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err := fn(h, tr); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// CleanPath normalises a tar entry name ("./usr/sbin/x" -> "usr/sbin/x").
func CleanPath(name string) string {
	return strings.TrimPrefix(strings.TrimPrefix(name, "."), "/")
}
