# Throwaway workspace that prints facts about the HCP Terraform worker.
# Run a plan and read the "recon" output in the run log. Plan-only is fine.

terraform {
  required_version = ">= 1.10"
  required_providers {
    external = {
      source  = "hashicorp/external"
      version = "~> 2.3"
    }
  }
}

data "external" "recon" {
  program = ["bash", "-c", <<-EOT
    out=$( {
      echo "## id";          id
      echo "## uname";       uname -a
      echo "## os-release";  cat /etc/os-release
      echo "## glibc";       ldd --version 2>&1 | head -1
      echo "## env";         echo "TMPDIR=$TMPDIR HOME=$HOME XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR"
      echo "## dirs";        ls -ld /run/user /tmp 2>&1; df -h /tmp
      echo "## libs";        for l in libcryptsetup.so.12 libdbus-1.so.3 libnl-3.so.200 libnl-route-3.so.200; do printf '%s: ' $l; ldconfig -p 2>/dev/null | grep -q "$l" && echo present || echo missing; done
      echo "## egress";      for u in https://packages.twingate.com/apt/Packages https://archive.ubuntu.com/ubuntu/dists/focal/Release https://twingate.com; do printf '%s ' $u; curl -s -o /dev/null -w '%%{http_code}\n' -m 10 $u; done
    } 2>&1 )
    printf '{"recon":"%s"}' "$(printf '%s' "$out" | base64 | tr -d '\n')"
  EOT
  ]
}

output "recon" {
  value = base64decode(data.external.recon.result.recon)
}
