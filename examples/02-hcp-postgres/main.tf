# End-to-end: open a Twingate tunnel to a private PostgreSQL server and
# manage a role through it, on HCP Terraform hosted workers.
#
# Workspace variables:
#   TWINGATE_SERVICE_KEY  env, sensitive  (service key JSON)
#   db_user               terraform
#   db_password           terraform, sensitive

terraform {
  required_version = ">= 1.10"

  cloud {
    organization = "<ORG>"
    workspaces {
      name = "twingate-tunnel-pg"
    }
  }

  required_providers {
    twingatetunnel = {
      # Binary committed at terraform.d/plugins/twingate.local/twingate/twingatetunnel/0.1.0/linux_amd64/
      source  = "twingate.local/twingate/twingatetunnel"
      version = "0.1.0"
    }
    postgresql = {
      source  = "cyrilgdn/postgresql"
      version = "~> 1.25"
    }
  }
}

variable "db_host" {
  type    = string
  default = "db.internal"
}

variable "db_user" {
  type = string
}

variable "db_password" {
  type      = string
  sensitive = true
}

provider "twingatetunnel" {
  # Pin for repeatable runs; omit for the newest client.
  # client_version = "2026.239.6882"
}

ephemeral "twingatetunnel_tcp" "db" {
  destination = "${var.db_host}:5432"
}

provider "postgresql" {
  host      = ephemeral.twingatetunnel_tcp.db.local_host
  port      = ephemeral.twingatetunnel_tcp.db.local_port
  username  = var.db_user
  password  = var.db_password
  sslmode   = "disable"
  superuser = false
}

resource "postgresql_role" "example" {
  name  = "tg_tunnel_demo"
  login = false
}
