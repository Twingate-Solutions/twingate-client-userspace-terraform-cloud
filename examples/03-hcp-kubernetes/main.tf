# Reach a private Kubernetes API through the Twingate HTTP proxy.
#
# Workspace variables:
#   TWINGATE_SERVICE_KEY  env, sensitive
#   k8s_host, k8s_ca, k8s_token (sensitive)

terraform {
  required_version = ">= 1.10"

  cloud {
    organization = "<ORG>"
    workspaces {
      name = "twingate-tunnel-k8s"
    }
  }

  required_providers {
    twingatetunnel = {
      # Binary committed at terraform.d/plugins/twingate.local/twingate/twingatetunnel/0.1.0/linux_amd64/
      source  = "twingate.local/twingate/twingatetunnel"
      version = "0.1.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.38"
    }
  }
}

variable "k8s_host" {
  description = "API server host as defined in the Twingate Resource, e.g. k8s.internal"
  type        = string
}

variable "k8s_ca" {
  description = "PEM CA certificate of the API server."
  type        = string
}

variable "k8s_token" {
  type      = string
  sensitive = true
}

provider "twingatetunnel" {}

ephemeral "twingatetunnel_http_proxy" "k8s" {
  probe_destination = "${var.k8s_host}:443"
}

provider "kubernetes" {
  host                   = "https://${var.k8s_host}"
  proxy_url              = ephemeral.twingatetunnel_http_proxy.k8s.proxy_url
  cluster_ca_certificate = var.k8s_ca
  token                  = var.k8s_token
}

resource "kubernetes_namespace" "example" {
  metadata {
    name = "tg-tunnel-demo"
  }
}
