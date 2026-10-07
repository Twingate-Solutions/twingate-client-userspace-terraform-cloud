# Creates the Twingate side: Remote Network, Connector (+ tokens), a Resource
# for the private database, and a Service Account with a key that can reach it.
#
# Auth: export TWINGATE_API_TOKEN and TWINGATE_NETWORK (tenant name).
# Deploy the Connector with the token outputs (Docker, Helm, systemd, ...).

terraform {
  required_version = ">= 1.10"
  required_providers {
    twingate = {
      source  = "Twingate/twingate"
      version = "~> 5.0"
    }
  }
}

provider "twingate" {}

variable "name" {
  type    = string
  default = "hcp-terraform"
}

variable "db_address" {
  description = "FQDN or IP of the private database, as the Connector resolves it."
  type        = string
  default     = "db.internal"
}

variable "db_port" {
  type    = string
  default = "5432"
}

resource "twingate_remote_network" "this" {
  name     = var.name
  location = "OTHER"
}

resource "twingate_connector" "this" {
  remote_network_id = twingate_remote_network.this.id
}

resource "twingate_connector_tokens" "this" {
  connector_id = twingate_connector.this.id
}

resource "twingate_service_account" "terraform" {
  name = "${var.name}-runner"
}

resource "twingate_service_account_key" "terraform" {
  name               = "${var.name}-runner"
  service_account_id = twingate_service_account.terraform.id
}

resource "twingate_resource" "db" {
  name              = "${var.name}-db"
  address           = var.db_address
  remote_network_id = twingate_remote_network.this.id

  protocols = {
    allow_icmp = false
    tcp = {
      policy = "RESTRICTED"
      ports  = [var.db_port]
    }
    udp = {
      policy = "DENY_ALL"
    }
  }

  access_service {
    service_account_id = twingate_service_account.terraform.id
  }
}

output "connector_access_token" {
  value     = twingate_connector_tokens.this.access_token
  sensitive = true
}

output "connector_refresh_token" {
  value     = twingate_connector_tokens.this.refresh_token
  sensitive = true
}

# Value for the TWINGATE_SERVICE_KEY workspace variable:
#   terraform output -raw service_key
output "service_key" {
  value     = twingate_service_account_key.terraform.token
  sensitive = true
}
