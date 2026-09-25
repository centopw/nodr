package proxmoxvm

// view is the part of a VirtualMachine that the provision lens maps to a
// proxmox_virtual_environment_vm resource of the bpg/proxmox provider. Field
// order is the order in which code is rendered.
//
// Attributes nodr always renders are required, so that removing one in
// code hands the value to the provider's default and makes the field
// code-owned. Optional attributes have a clear meaning when absent.
type view struct {
	Name         string   `hcl:"name"`
	NodeName     string   `hcl:"node_name"`
	VMID         int      `hcl:"vm_id"`
	Tags         []string `hcl:"tags,set,optional"`
	OnBoot       bool     `hcl:"on_boot"`
	Started      *bool    `hcl:"started,optional"`
	Protection   bool     `hcl:"protection"`
	Machine      string   `hcl:"machine,optional"`
	BIOS         string   `hcl:"bios,optional"`
	SCSIHardware string   `hcl:"scsi_hardware,optional"`

	Clone  *cloneView `hcl:"clone,block,optional"`
	Agent  *agentView `hcl:"agent,block"`
	CPU    *cpuView   `hcl:"cpu,block"`
	Memory *memView   `hcl:"memory,block"`
	Disks  []diskView `hcl:"disk,block"`
	NICs   []nicView  `hcl:"network_device,block"`
	Init   *initView  `hcl:"initialization,block,optional"`
}

type cloneView struct {
	VMID int `hcl:"vm_id"`
	// NodeName is the node that holds the template. It is empty if the
	// template does not name its node, and the provider then looks for the
	// template on the node of the VM.
	NodeName string `hcl:"node_name,optional"`
	Full     bool   `hcl:"full"`
}

type agentView struct {
	Enabled bool `hcl:"enabled"`
}

type cpuView struct {
	Cores   int    `hcl:"cores"`
	Sockets int    `hcl:"sockets"`
	Type    string `hcl:"type"`
}

type memView struct {
	// Dedicated and Floating are in MiB. Floating 0 disables ballooning.
	Dedicated int `hcl:"dedicated"`
	Floating  int `hcl:"floating,optional"`
}

type diskView struct {
	DatastoreID string `hcl:"datastore_id"`
	Interface   string `hcl:"interface"`
	// Size is in GiB.
	Size     int    `hcl:"size"`
	Discard  string `hcl:"discard"`
	SSD      bool   `hcl:"ssd,optional"`
	IOThread bool   `hcl:"iothread,optional"`
}

type nicView struct {
	Bridge     string `hcl:"bridge"`
	VLANID     int    `hcl:"vlan_id,optional"`
	MACAddress string `hcl:"mac_address,optional"`
	Model      string `hcl:"model"`
}

type initView struct {
	DatastoreID string           `hcl:"datastore_id,optional"`
	IPConfigs   []ipConfigView   `hcl:"ip_config,block"`
	User        *userAccountView `hcl:"user_account,block,optional"`
}

type ipConfigView struct {
	IPv4 *ipv4View `hcl:"ipv4,block,optional"`
}

type ipv4View struct {
	Address string `hcl:"address"`
	Gateway string `hcl:"gateway,optional"`
}

type userAccountView struct {
	Username string   `hcl:"username,optional"`
	Keys     []string `hcl:"keys,set,optional"`
}
