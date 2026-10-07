package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxIndexBytes = 64 << 20
	maxDebBytes   = 256 << 20
)

// DefaultHTTPClient is used when Options.HTTPClient is nil. Redirects are
// followed (packages.twingate.com redirects .deb downloads to S3).
var DefaultHTTPClient = &http.Client{Timeout: 5 * time.Minute}

func get(ctx context.Context, hc *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "terraform-provider-twingatetunnel")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: unexpected status %s", url, resp.Status)
	}
	return resp, nil
}

// fetchBytes downloads url into memory. When wantSHA256 is non-empty the
// content must match it.
func fetchBytes(ctx context.Context, hc *http.Client, url, wantSHA256 string, limit int64) ([]byte, error) {
	resp, err := get(ctx, hc, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", url, limit)
	}
	if wantSHA256 != "" {
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, wantSHA256) {
			return nil, fmt.Errorf("checksum mismatch for %s: got %s, want %s", url, got, wantSHA256)
		}
	}
	return b, nil
}

// fetchFile downloads url to a temporary file in dir, verifies its SHA-256 and
// returns the path. The caller removes the file.
func fetchFile(ctx context.Context, hc *http.Client, url, wantSHA256, dir string) (string, error) {
	if wantSHA256 == "" {
		return "", errors.New("refusing to download without a SHA-256 to verify against")
	}
	resp, err := get(ctx, hc, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	f, err := os.CreateTemp(dir, "download-*.deb")
	if err != nil {
		return "", err
	}
	path := f.Name()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxDebBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxDebBytes {
		err = fmt.Errorf("%s exceeds %d bytes", url, maxDebBytes)
	}
	if err == nil {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantSHA256) {
			err = fmt.Errorf("checksum mismatch for %s: got %s, want %s", url, got, wantSHA256)
		}
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// writeFileAtomic writes r to path via a temp file in the same directory.
func writeFileAtomic(path string, r io.Reader, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
