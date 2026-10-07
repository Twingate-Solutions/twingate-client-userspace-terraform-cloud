package client

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/debian"
)

// libSource is a frozen Debian-format repository index used to supply shared
// libraries that the host lacks. Ubuntu 20.04 (focal) release pockets never
// change, so the index hash is pinned, and every .deb is then verified against
// the SHA256 recorded in that index. Focal's glibc is 2.31, so its libraries
// load on any newer glibc host.
type libSource struct {
	BaseURL     string
	IndexPath   string
	IndexSHA256 string
	Arch        string
}

var libSources = map[string]libSource{
	"amd64": {
		BaseURL:     "https://archive.ubuntu.com/ubuntu/",
		IndexPath:   "dists/focal/main/binary-amd64/Packages.gz",
		IndexSHA256: "f25bb719a900d962a4df25cbb20f0a54a23d9f16c3fcdc4f4872ead131f5a604",
		Arch:        "amd64",
	},
	"arm64": {
		BaseURL:     "https://ports.ubuntu.com/ubuntu-ports/",
		IndexPath:   "dists/focal/main/binary-arm64/Packages.gz",
		IndexSHA256: "7bc7c3ed144d6ef0cc818a7ebfa34d9e36bfd90f9d9eaa3a362cb802bede6451",
		Arch:        "arm64",
	},
}

// sonamePackages maps each shared library in twingated's dependency closure
// to the focal package that ships it.
var sonamePackages = map[string]string{
	"libcryptsetup.so.12":    "libcryptsetup12",
	"libdbus-1.so.3":         "libdbus-1-3",
	"libnl-3.so.200":         "libnl-3-200",
	"libnl-route-3.so.200":   "libnl-route-3-200",
	"libdevmapper.so.1.02.1": "libdevmapper1.02.1",
	"libargon2.so.1":         "libargon2-1",
	"libjson-c.so.4":         "libjson-c4",
	"libcrypto.so.1.1":       "libssl1.1",
	"libssl.so.1.1":          "libssl1.1",
	"libblkid.so.1":          "libblkid1",
	"libuuid.so.1":           "libuuid1",
	"libsystemd.so.0":        "libsystemd0",
	"libselinux.so.1":        "libselinux1",
	"libudev.so.1":           "libudev1",
	"libcap.so.2":            "libcap2",
	"libgcrypt.so.20":        "libgcrypt20",
	"libgpg-error.so.0":      "libgpg-error0",
	"liblzma.so.5":           "liblzma5",
	"libzstd.so.1":           "libzstd1",
	"liblz4.so.1":            "liblz4-1",
	"libpcre2-8.so.0":        "libpcre2-8-0",
}

var missingLib = regexp.MustCompile(`error while loading shared libraries: ([^:\s]+): cannot open shared object file`)

// LibraryPathEnv returns the LD_LIBRARY_PATH value for running twingated
// with libDir taking precedence over the inherited value.
func LibraryPathEnv(libDir string) string {
	if cur := os.Getenv("LD_LIBRARY_PATH"); cur != "" {
		return libDir + string(os.PathListSeparator) + cur
	}
	return libDir
}

// probe runs "twingated --version". It returns the first missing shared
// library reported by the dynamic loader, or "" if the binary ran.
func probe(ctx context.Context, bin, libDir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+LibraryPathEnv(libDir))
	out, err := cmd.CombinedOutput()
	if err == nil {
		return "", nil
	}
	if m := missingLib.FindSubmatch(out); m != nil {
		return string(m[1]), nil
	}
	return "", fmt.Errorf("%s --version failed: %w: %s", bin, err, strings.TrimSpace(string(out)))
}

func ensureLibraries(ctx context.Context, o Options, inst *Installation) error {
	var (
		index   []debian.Stanza
		fetched = map[string]bool{}
	)
	for range 64 {
		lib, err := probe(ctx, inst.BinPath, inst.LibDir)
		if err != nil {
			return err
		}
		if lib == "" {
			return nil
		}
		if !o.FetchLibraries {
			return fmt.Errorf("twingated needs %s, which is not installed; enable fetch_missing_libraries or install it", lib)
		}
		pkg, ok := sonamePackages[lib]
		if !ok {
			return fmt.Errorf("twingated needs %s, which is not installed and has no known source package", lib)
		}
		if fetched[pkg] {
			return fmt.Errorf("installed %s but %s is still not loadable", pkg, lib)
		}
		src, ok := libSources[runtime.GOARCH]
		if !ok {
			return fmt.Errorf("no library source for %s; install %s", runtime.GOARCH, lib)
		}
		if index == nil {
			if index, err = loadLibIndex(ctx, o, src); err != nil {
				return err
			}
		}
		o.Logf("host lacks %s; fetching %s from %s", lib, pkg, src.BaseURL)
		if err := installLibPackage(ctx, o, src, index, pkg, inst.LibDir); err != nil {
			return fmt.Errorf("fetch %s for %s: %w", pkg, lib, err)
		}
		fetched[pkg] = true
	}
	return errors.New("too many missing shared libraries")
}

func loadLibIndex(ctx context.Context, o Options, src libSource) ([]debian.Stanza, error) {
	gz, err := fetchBytes(ctx, o.HTTPClient, src.BaseURL+src.IndexPath, src.IndexSHA256, maxIndexBytes)
	if err != nil {
		return nil, fmt.Errorf("fetch library index: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("library index: %w", err)
	}
	defer func() { _ = zr.Close() }()
	return debian.ParsePackages(zr)
}

func installLibPackage(ctx context.Context, o Options, src libSource, index []debian.Stanza, pkg, libDir string) error {
	s := debian.Select(index, pkg, src.Arch, "")
	if s == nil {
		return fmt.Errorf("package %s not in index", pkg)
	}
	debPath, err := fetchFile(ctx, o.HTTPClient, src.BaseURL+s.Filename(), s.SHA256(), libDir)
	if err != nil {
		return err
	}
	defer os.Remove(debPath)
	f, err := os.Open(debPath)
	if err != nil {
		return err
	}
	defer f.Close()

	n := 0
	err = debian.WalkData(f, func(h *tar.Header, body io.Reader) error {
		name := debian.CleanPath(h.Name)
		base := path.Base(name)
		if !strings.HasSuffix(path.Dir(name), "-linux-gnu") || !strings.HasPrefix(base, "lib") || !strings.Contains(base, ".so") {
			return nil
		}
		dst := filepath.Join(libDir, base)
		switch h.Typeflag {
		case tar.TypeReg:
			n++
			return writeFileAtomic(dst, body, 0o755)
		case tar.TypeSymlink:
			n++
			_ = os.Remove(dst)
			return os.Symlink(path.Base(h.Linkname), dst)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("no shared libraries found in package")
	}
	return nil
}
