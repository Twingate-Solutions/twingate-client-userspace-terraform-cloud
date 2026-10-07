# Twingate Tunnel for HCP Terraform

> ⚠️ **Example project — provided as-is, with no support or warranty.** This project is published as a reference example to build from, not a supported product. Nothing here is guaranteed and no support is attached to it. It was developed with help from an LLM-based coding assistant. Review the code and test it yourself before using it in any critical or production environment. Your use is governed by the [Apache License 2.0](LICENSE), including its "AS IS", no-warranty (Section 7), and limitation-of-liability (Section 8) terms.

A Terraform provider that lets HCP Terraform runs on HashiCorp-hosted workers reach private resources (databases, Kubernetes APIs, SSH) through Twingate. During each plan and apply it starts the Twingate Client in userspace mode with a Service Account key, and opens a local tunnel that other providers connect to. It needs no root, custom image or self-hosted agent.

---

## Requirements

| Item | Value |
|---|---|
| Terraform | `>= 1.10` (workspace setting) |
| Twingate | Service Account + key with access to the Resource |
| Worker | Linux x86_64/arm64 (HCP hosted workers qualify) |
| Egress from worker | `packages.twingate.com`, `s3.amazonaws.com`, `archive.ubuntu.com` (443); Twingate Controller/Relays (443, 30000-31000) |

---

## 1. Twingate: Resource + Service Account key

Using Terraform ([examples/01-bootstrap-twingate](examples/01-bootstrap-twingate/main.tf)):

```bash
export TWINGATE_API_TOKEN=...   # Read, Write & Provision
export TWINGATE_NETWORK=acme    # acme.twingate.com
cd examples/01-bootstrap-twingate
terraform init && terraform apply -var db_address=db.internal
terraform output -raw service_key   # value for TWINGATE_SERVICE_KEY
```

The core of it:

```hcl
resource "twingate_service_account" "terraform" {
  name = "hcp-terraform-runner"
}

resource "twingate_service_account_key" "terraform" {
  service_account_id = twingate_service_account.terraform.id
}

resource "twingate_resource" "db" {
  name              = "hcp-terraform-db"
  address           = "db.internal"
  remote_network_id = twingate_remote_network.this.id
  access_service {
    service_account_id = twingate_service_account.terraform.id
  }
}
```

---

## 2. Build and host the provider yourself

No published builds. Build from this repo:

```bash
VERSION=0.1.0
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-X main.version=$VERSION" \
  -o terraform-provider-twingatetunnel_v$VERSION .
```

Then make it available to HCP Terraform, either way:

**A. Your HCP private registry.** See [Publish private providers](https://developer.hashicorp.com/terraform/cloud-docs/registry/publish-providers). Requires a GPG-signed SHA256SUMS; [.goreleaser.yml](.goreleaser.yml) builds the zips and checksums (add a `signs` block). Use `source = "app.terraform.io/<ORG>/twingatetunnel"`.

**B. Commit it with your Terraform config.** HCP Terraform loads providers from `terraform.d/plugins/` in the working directory.

```text
your-config-repo/
├── main.tf
└── terraform.d/plugins/twingate.local/twingate/twingatetunnel/0.1.0/linux_amd64/
    └── terraform-provider-twingatetunnel_v0.1.0
```

Use `source = "twingate.local/twingate/twingatetunnel"`, `version = "0.1.0"` (must match the path).

---

## 3. Workspace variables

| Key | Category | Sensitive |
|---|---|---|
| `TWINGATE_SERVICE_KEY` | env | yes |
| your DB/API credentials | terraform | yes |

---

## 4. Terraform configuration

### Raw TCP (PostgreSQL, MySQL, SSH, ...)

```hcl
terraform {
  required_version = ">= 1.10"
  required_providers {
    twingatetunnel = {
      source  = "twingate.local/twingate/twingatetunnel"   # or app.terraform.io/<ORG>/twingatetunnel
      version = "0.1.0"
    }
    postgresql = { source = "cyrilgdn/postgresql" }
  }
}

provider "twingatetunnel" {
  client_version = "2026.239.6882"   # optional pin
}

ephemeral "twingatetunnel_tcp" "db" {
  destination = "db.internal:5432"
}

provider "postgresql" {
  host     = ephemeral.twingatetunnel_tcp.db.local_host
  port     = ephemeral.twingatetunnel_tcp.db.local_port
  username = var.db_user
  password = var.db_password
  sslmode  = "disable"
}
```

Full example: [examples/02-hcp-postgres](examples/02-hcp-postgres/main.tf)

### HTTP proxy (Kubernetes, HTTP APIs)

```hcl
ephemeral "twingatetunnel_http_proxy" "k8s" {
  probe_destination = "k8s.internal:443"
}

provider "kubernetes" {
  host      = "https://k8s.internal"
  proxy_url = ephemeral.twingatetunnel_http_proxy.k8s.proxy_url
  # ...credentials
}
```

Full example: [examples/03-hcp-kubernetes](examples/03-hcp-kubernetes/main.tf)

### Multiple Resources

```hcl
ephemeral "twingatetunnel_tcp" "pg"    { destination = "pg.internal:5432" }
ephemeral "twingatetunnel_tcp" "mysql" { destination = "mysql.internal:3306" }
```

---

## Reference

### `provider "twingatetunnel"`

| Attribute | Default | Description |
|---|---|---|
| `service_key` | env `TWINGATE_SERVICE_KEY` | Service Account key JSON (sensitive) |
| `client_version` | newest | `twingate` package version from the Twingate apt repo |
| `client_path` | | Use a preinstalled `twingated`; skips download |
| `client_url` / `client_sha256` | | Download the `.deb` from your own mirror |
| `fetch_missing_libraries` | `true` | Fetch shared libs the host lacks (Ubuntu 20.04 archive, SHA-256 verified) |
| `log_dir` | `/tmp/twingatetunnel/logs` | Supervisor and `twingated` logs |
| `cache_dir` | `/tmp/twingatetunnel/cache` | Downloaded client and libs |

### `ephemeral "twingatetunnel_tcp"`

| Attribute | Kind | Description |
|---|---|---|
| `destination` | required | `host:port` of the Twingate Resource |
| `local_host` | optional/computed | default `127.0.0.1` |
| `local_port` | optional/computed | default random free port |
| `ready_timeout` | optional | default `90s` |
| `proxy_url` | computed | underlying Twingate HTTP proxy |

### `ephemeral "twingatetunnel_http_proxy"`

| Attribute | Kind | Description |
|---|---|---|
| `probe_destination` | optional | `host:port` that must be reachable before ready |
| `ready_timeout` | optional | default `90s` |
| `proxy_url` / `proxy_host` / `proxy_port` | computed | proxy address |

Generated docs: [docs/](docs/)

---

## Local testing

```bash
scripts/test.sh            # gofmt, vet, unit + acceptance tests in Docker, non-root
```

Run a config locally (Linux): place the binary under `terraform.d/plugins/...` as in step 2, remove the `cloud` block, then:

```bash
export TWINGATE_SERVICE_KEY="$(cat service_key.json)"
terraform init && terraform plan
```

---

## Troubleshooting

| Symptom | Check |
|---|---|
| `Twingate tunnel failed to start` | Error includes the last log lines; full logs in `log_dir/tunnel-*/` |
| `twingated state: Authenticating` / `Error` | Service key invalid/expired, or no egress to the Twingate Controller |
| Online but `tunnel to ... closed` | Service Account lacks access to the Resource, or `destination` doesn't match the Resource address/port |
| `needs libX.so, which is not installed` | Set `fetch_missing_libraries = true` or install the lib |
| Probe the worker | Plan [examples/00-recon](examples/00-recon/main.tf) in a throwaway workspace |

---

## License

[Apache License 2.0](LICENSE)
