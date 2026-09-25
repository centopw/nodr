# Managed by nodr.
terraform {
  required_version = ">= 1.8"

  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.80, < 1.0"
    }
  }
}
