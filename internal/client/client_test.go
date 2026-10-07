package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/testutil"
)

// fakeRepo serves a Twingate-style flat apt repo with one twingate package
// whose twingated is a shell script.
func fakeRepo(t *testing.T, indexSHA string) (*httptest.Server, *int) {
	t.Helper()
	deb := testutil.BuildDeb(t, ".gz", []testutil.DebFile{
		{Name: "./usr/sbin/twingated", Body: "#!/bin/sh\necho 2026.1.1\n", Mode: 0o755},
	})
	sum := sha256.Sum256(deb)
	if indexSHA == "" {
		indexSHA = hex.EncodeToString(sum[:])
	}
	downloads := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/apt/Packages", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Package: twingate\nArchitecture: amd64\nVersion: 2026.1.1\nFilename: files/x/twingate-amd64.deb\nSHA256: %s\n\n"+
			"Package: twingate\nArchitecture: arm64\nVersion: 2026.1.1\nFilename: files/x/twingate-amd64.deb\nSHA256: %s\n\n"+
			"Package: twingate\nArchitecture: amd64\nVersion: 2025.1.1\nFilename: files/old.deb\nSHA256: 00\n", indexSHA, indexSHA)
	})
	mux.HandleFunc("/apt/files/x/twingate-amd64.deb", func(w http.ResponseWriter, r *http.Request) {
		// Mirror packages.twingate.com, which redirects to object storage.
		http.Redirect(w, r, "/s3/deb", http.StatusFound)
	})
	mux.HandleFunc("/s3/deb", func(w http.ResponseWriter, r *http.Request) {
		downloads++
		w.Write(deb)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &downloads
}

func TestEnsureDownloadsVerifiesAndCaches(t *testing.T) {
	srv, downloads := fakeRepo(t, "")
	opts := Options{RepoURL: srv.URL + "/apt", CacheDir: t.TempDir()}

	inst, err := Ensure(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Version != "2026.1.1" {
		t.Errorf("version = %q", inst.Version)
	}
	if fi, err := os.Stat(inst.BinPath); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("binary not executable: %v %v", fi, err)
	}

	opts.Version = "2026.1.1"
	if _, err := Ensure(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if *downloads != 1 {
		t.Errorf("pinned cached version downloaded again: %d downloads", *downloads)
	}
}

func TestEnsureRejectsChecksumMismatch(t *testing.T) {
	srv, _ := fakeRepo(t, strings.Repeat("ab", 32))
	_, err := Ensure(context.Background(), Options{RepoURL: srv.URL + "/apt", CacheDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
}

func TestEnsureUnknownVersion(t *testing.T) {
	srv, _ := fakeRepo(t, "")
	_, err := Ensure(context.Background(), Options{RepoURL: srv.URL + "/apt", CacheDir: t.TempDir(), Version: "1.2.3"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestProbeReportsMissingLibrary(t *testing.T) {
	dir := t.TempDir()
	bin := dir + "/twingated"
	script := "#!/bin/sh\necho \"$0: error while loading shared libraries: libnl-3.so.200: cannot open shared object file: No such file or directory\" >&2\nexit 127\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	lib, err := probe(context.Background(), bin, dir)
	if err != nil || lib != "libnl-3.so.200" {
		t.Fatalf("got %q, %v", lib, err)
	}
	err = ensureLibraries(context.Background(), Options{}, &Installation{BinPath: bin, LibDir: dir})
	if err == nil || !strings.Contains(err.Error(), "fetch_missing_libraries") {
		t.Fatalf("want hint about fetch_missing_libraries, got %v", err)
	}
}
