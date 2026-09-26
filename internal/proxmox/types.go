package proxmox

// Version holds output from GET /version.
type Version struct {
	Release string `json:"release"`
	RepoID  string `json:"repoid"`
	Version string `json:"version"`
}

// ClusterNode holds one node's status from GET /cluster/status.
type ClusterNode struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"` // "cluster" or "node"
	IP     string `json:"ip"`
	Online int    `json:"online"`
	Level  string `json:"level"`
	Local  int    `json:"local"`
	NodeID int    `json:"nodeid"`
}

// ClusterResource holds one resource from GET /cluster/resources?type=vm.
type ClusterResource struct {
	ID       string `json:"id"`       // "qemu/100"
	VMID     int    `json:"vmid"`     // 100
	Name     string `json:"name"`     // VM name
	Node     string `json:"node"`     // node name
	Type     string `json:"type"`     // "qemu"
	Status   string `json:"status"`   // "running", "stopped"
	MaxMem   int64  `json:"maxmem"`   // bytes
	MaxDisk  int64  `json:"maxdisk"`  // bytes
	MaxCPU   int    `json:"maxcpu"`   // cores
	Template int    `json:"template"` // 1 if template
	Pool     string `json:"pool"`
}

// QEMUSummary holds guest overview from GET /nodes/{node}/qemu.
type QEMUSummary struct {
	VMID      int    `json:"vmid"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CPUs      int    `json:"cpus"`
	MaxMem    int64  `json:"maxmem"`
	MaxDisk   int64  `json:"maxdisk"`
	Uptime    int64  `json:"uptime"`
	NetIn     int64  `json:"netin"`
	NetOut    int64  `json:"netout"`
	DiskRead  int64  `json:"diskread"`
	DiskWrite int64  `json:"diskwrite"`
}

// QEMUConfig holds raw VM configuration from GET /nodes/{node}/qemu/{vmid}/config.
type QEMUConfig struct {
	Digest      string            `json:"digest"` // SHA1 digest for optimistic locking
	RawSettings map[string]string `json:"-"`      // dynamically decoded key-values
}

// CertificateInfo holds output from GET /nodes/{node}/certificates/info.
type CertificateInfo struct {
	Fingerprint string `json:"fingerprint"`
	Subject     string `json:"subject"`
	Issuer      string `json:"issuer"`
	NotBefore   int64  `json:"notbefore"`
	NotAfter    int64  `json:"notafter"`
}

// UserOptions specifies options for creating a user via POST /access/users.
type UserOptions struct {
	Comment  string `json:"comment,omitempty"`
	Email    string `json:"email,omitempty"`
	Enable   int    `json:"enable,omitempty"`
	Expire   int64  `json:"expire,omitempty"`
	Password string `json:"password,omitempty"`
}

// APITokenResult holds the secret returned upon creating an API token.
type APITokenResult struct {
	Value string `json:"value"`
}
