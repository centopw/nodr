# Managed by nodr. The API token comes from the environment variable
# PROXMOX_VE_API_TOKEN, which nodr sets from the cluster's credentialsRef
# (proxmox/pve-main-token) when it runs OpenTofu.
provider "proxmox" {
  endpoint = "https://10.0.10.11:8006/"
}
