#!/usr/bin/env bash
# Run gofmt, go vet and all tests (including acceptance tests with a fake
# twingated) inside a Linux container as a non-root user.
# Usage: scripts/test.sh [extra go test args]
set -euo pipefail

GO_IMAGE="${GO_IMAGE:-golang:1.27-bookworm}"
TF_VERSION="${TF_VERSION:-1.16.5}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if command -v cygpath >/dev/null 2>&1; then ROOT="$(cygpath -w "$ROOT")"; fi

MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$ROOT:/src" -v twingatetunnel-gomod:/go/pkg/mod -w /src \
  -e TF_VERSION="$TF_VERSION" -e EXTRA_ARGS="$*" \
  "$GO_IMAGE" bash -euc '
    apt-get update -qq >/dev/null && apt-get install -y -qq unzip >/dev/null
    curl -sSfLo /tmp/tf.zip "https://releases.hashicorp.com/terraform/${TF_VERSION}/terraform_${TF_VERSION}_linux_amd64.zip"
    unzip -qo /tmp/tf.zip -d /usr/local/bin
    go mod download
    useradd -m -u 1000 tester 2>/dev/null || true
    chown -R 1000 /go/pkg/mod
    su tester -c "
      set -e
      export PATH=/usr/local/go/bin:/usr/local/bin:\$PATH GOMODCACHE=/go/pkg/mod
      cd /src
      test -z \"\$(gofmt -l .)\" || { gofmt -l .; echo gofmt needed; exit 1; }
      go vet ./...
      TF_ACC=1 go test -count=1 ./... $EXTRA_ARGS
    "
  '
