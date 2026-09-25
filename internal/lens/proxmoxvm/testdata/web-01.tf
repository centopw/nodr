# nodr:managed vm/web-01
resource "proxmox_virtual_environment_vm" "web_01" {
  name          = "web-01"
  node_name     = "pve2"
  vm_id         = 1012
  tags          = ["app-website", "env-prod", "nodr"]
  on_boot       = true
  started       = true
  protection    = true
  machine       = "q35"
  bios          = "ovmf"
  scsi_hardware = "virtio-scsi-single"

  clone {
    vm_id = 9001
    full  = true
  }

  agent {
    enabled = true
  }

  cpu {
    cores   = 2
    sockets = 1
    type    = "x86-64-v2-AES"
  }

  memory {
    dedicated = 4096
    floating  = 2048
  }

  disk {
    datastore_id = "ceph-vm"
    interface    = "scsi0"
    size         = 32
    discard      = "on"
    ssd          = true
    iothread     = true
  }

  network_device {
    bridge      = "vmbr0"
    vlan_id     = 20
    mac_address = "BC:24:11:3A:5E:01"
    model       = "virtio"
  }

  initialization {
    datastore_id = "ceph-vm"

    ip_config {
      ipv4 {
        address = "10.0.20.21/24"
        gateway = "10.0.20.1"
      }
    }

    user_account {
      username = "ops"
      keys     = ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOpsTeamKeyForTestsOnly ops@example"]
    }
  }
}
