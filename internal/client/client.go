// Package client obtains a runnable twingated binary without root: it reads
// the Twingate apt index, downloads and verifies the .deb, extracts
// usr/sbin/twingated, and fills in any shared libraries the host lacks.
package client

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/debian"
)

const (
	// DefaultRepoURL is the Twingate apt repository (flat layout).
	DefaultRepoURL = "https://packages.twingate.com/apt/"
	packageName    = "twingate"
	binaryInDeb    = "usr/sbin/twingated"
)

// Options controls how twingated is obtained.
type Options struct {
	// Path, if set, is a preinstalled twingated. Nothing is downloaded.
	Path string
	// Version pins the twingate package version. Empty selects the highest.
	Version string
	// DebURL and DebSHA256 override the apt index with a specific .deb.
	DebURL    string
	DebSHA256 string
	// RepoURL overrides DefaultRepoURL.
	RepoURL string
	// CacheDir holds extracted binaries and libraries.
	CacheDir string
	// FetchLibraries downloads shared libraries that the host is missing.
	FetchLibraries bool
	HTTPClient     *http.Client
	// Logf receives progress messages. It must not write to stdout.
	Logf func(format string, args ...any)
}

// Installation is a runnable twingated.
type Installation struct {
	BinPath string
	// LibDir must be prepended to LD_LIBRARY_PATH when running BinPath.
	LibDir  string
	Version string
}

// Ensure returns a runnable twingated, downloading what is needed.
func Ensure(ctx context.Context, o Options) (*Installation, error) {
	if o.HTTPClient == nil {
		o.HTTPClient = DefaultHTTPClient
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.RepoURL == "" {
		o.RepoURL = DefaultRepoURL
	}
	if !strings.HasSuffix(o.RepoURL, "/") {
		o.RepoURL += "/"
	}
	if o.CacheDir == "" {
		return nil, errors.New("cache directory is required")
	}
	if err := os.MkdirAll(o.CacheDir, 0o700); err != nil {
		return nil, err
	}

	inst := &Installation{LibDir: filepath.Join(o.CacheDir, "lib")}
	if err := os.MkdirAll(inst.LibDir, 0o700); err != nil {
		return nil, err
	}

	if o.Path != "" {
		inst.BinPath = o.Path
		inst.Version = "preinstalled"
	} else {
		bin, ver, err := fetchBinary(ctx, o)
		if err != nil {
			return nil, err
		}
		inst.BinPath, inst.Version = bin, ver
	}

	if err := ensureLibraries(ctx, o, inst); err != nil {
		return nil, err
	}
	return inst, nil
}

func debArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH, nil
	}
	return "", fmt.Errorf("unsupported architecture %s", runtime.GOARCH)
}

var (
	safeVersion = regexp.MustCompile(`[^A-Za-z0-9.+~_-]`)
	sha256Hex   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func fetchBinary(ctx context.Context, o Options) (string, string, error) {
	debURL, debSHA, version := o.DebURL, strings.ToLower(o.DebSHA256), o.Version

	if debURL == "" {
		arch, err := debArch()
		if err != nil {
			return "", "", err
		}
		// Pinned versions that are already cached need no network access.
		if version != "" {
			if p := cachedBinary(o.CacheDir, version); p != "" {
				return p, version, nil
			}
		}
		idx, err := fetchBytes(ctx, o.HTTPClient, o.RepoURL+"Packages", "", maxIndexBytes)
		if err != nil {
			return "", "", fmt.Errorf("fetch Twingate package index: %w", err)
		}
		stanzas, err := debian.ParsePackages(bytes.NewReader(idx))
		if err != nil {
			return "", "", fmt.Errorf("parse Twingate package index: %w", err)
		}
		s := debian.Select(stanzas, packageName, arch, version)
		if s == nil {
			if version != "" {
				return "", "", fmt.Errorf("twingate version %q for %s not found in %sPackages", version, arch, o.RepoURL)
			}
			return "", "", fmt.Errorf("no twingate package for %s in %sPackages", arch, o.RepoURL)
		}
		if s.SHA256() == "" || s.Filename() == "" {
			return "", "", fmt.Errorf("index entry for twingate %s lacks Filename or SHA256", s.Version())
		}
		debURL, debSHA, version = o.RepoURL+s.Filename(), s.SHA256(), s.Version()
	} else if !sha256Hex.MatchString(debSHA) {
		return "", "", errors.New("client_sha256 must be a 64-character hex SHA-256 when client_url is set")
	} else if version == "" {
		version = "sha256-" + debSHA[:16]
	}

	if p := cachedBinary(o.CacheDir, version); p != "" {
		return p, version, nil
	}

	o.Logf("downloading twingate %s from %s", version, debURL)
	dir := filepath.Join(o.CacheDir, "bin", safeVersion.ReplaceAllString(version, "_"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	debPath, err := fetchFile(ctx, o.HTTPClient, debURL, debSHA, dir)
	if err != nil {
		return "", "", fmt.Errorf("download twingate package: %w", err)
	}
	defer os.Remove(debPath)

	f, err := os.Open(debPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	bin := filepath.Join(dir, "twingated")
	found := false
	err = debian.WalkData(f, func(h *tar.Header, body io.Reader) error {
		if h.Typeflag != tar.TypeReg || debian.CleanPath(h.Name) != binaryInDeb {
			return nil
		}
		found = true
		if err := writeFileAtomic(bin, body, 0o700); err != nil {
			return err
		}
		return io.EOF
	})
	if err != nil {
		return "", "", fmt.Errorf("extract twingated: %w", err)
	}
	if !found {
		return "", "", fmt.Errorf("%s not found in package", binaryInDeb)
	}
	return bin, version, nil
}

func cachedBinary(cacheDir, version string) string {
	p := filepath.Join(cacheDir, "bin", safeVersion.ReplaceAllString(version, "_"), "twingated")
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		return p
	}
	return ""
}
