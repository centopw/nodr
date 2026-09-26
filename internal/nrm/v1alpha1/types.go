// Package v1alpha1 defines the nodr/v1alpha1 kinds: their Go types, JSON
// Schemas, defaults, references and semantic checks.
//
// The JSON Schemas in schemas/ are the source of truth for the document
// format: they drive validation, editor completion and GUI forms. The Go
// types mirror them, and tests keep the two in step.
package v1alpha1

import (
	"encoding/json"
	"fmt"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/quantity"
)

// APIVersion is the API version of the kinds in this package.
const APIVersion = "nodr/v1alpha1"

// Kind names.
const (
	KindWorkspace      = "Workspace"
	KindProxmoxCluster = "ProxmoxCluster"
	KindNetwork        = "Network"
	KindTemplate       = "Template"
	KindSSHKey         = "SSHKey"
	KindVirtualMachine = "VirtualMachine"
)

// Kinds that the design defines but this API version does not implement
// yet. References to them are allowed but not checked.
const (
	kindConfigProfile = "ConfigProfile"
	kindBackupPolicy  = "BackupPolicy"
	kindUpdatePolicy  = "UpdatePolicy"
	kindRouter        = "Router"
)

// Object is a decoded document of one kind.
type Object[S any] struct {
	Metadata nrm.Metadata
	Spec     S
	// Document is the document the object was decoded from.
	Document *nrm.Document
}

// Ref returns the reference that identifies the object.
func (o *Object[S]) Ref() nrm.Ref { return o.Document.Ref() }

// Decode decodes a document into an object with spec type S.
func Decode[S any](d *nrm.Document) (*Object[S], error) {
	var spec S
	if err := d.DecodeSpec(&spec); err != nil {
		return nil, fmt.Errorf("decode %s: %w", d.Ref(), err)
	}
	return &Object[S]{Metadata: d.Metadata, Spec: spec, Document: d}, nil
}

type (
	// Workspace is a decoded workspace manifest.
	Workspace = Object[WorkspaceSpec]
	// ProxmoxCluster is a decoded ProxmoxCluster document.
	ProxmoxCluster = Object[ProxmoxClusterSpec]
	// Network is a decoded Network document.
	Network = Object[NetworkSpec]
	// Template is a decoded Template document.
	Template = Object[TemplateSpec]
	// SSHKey is a decoded SSHKey document.
	SSHKey = Object[SSHKeySpec]
	// VirtualMachine is a decoded VirtualMachine document.
	VirtualMachine = Object[VirtualMachineSpec]
)

// WorkspaceSpec is the spec of the workspace manifest, nodr.yaml.
type WorkspaceSpec struct {
	Environments     map[string]Environment `json:"environments,omitempty"`
	Engines          *Engines               `json:"engines,omitempty"`
	Bindings         map[string]string      `json:"bindings,omitempty"`
	BindingOverrides []BindingOverride      `json:"bindingOverrides,omitempty"`
	StateUnits       *StateUnits            `json:"stateUnits,omitempty"`
	Defaults         map[string]any         `json:"defaults,omitempty"`
	Git              *Git                   `json:"git,omitempty"`
}

// Environment configures one environment of a workspace.
type Environment struct {
	// VMIDRange holds the first and last Proxmox guest ID of the range.
	VMIDRange []int `json:"vmidRange,omitempty"`
}

// Engines holds version constraints for the engines a workspace uses.
type Engines struct {
	OpenTofu  *EngineVersion `json:"opentofu,omitempty"`
	Terraform *EngineVersion `json:"terraform,omitempty"`
	Ansible   *AnsibleEngine `json:"ansible,omitempty"`
}

// EngineVersion is a version constraint.
type EngineVersion struct {
	Version string `json:"version,omitempty"`
}

// AnsibleEngine is the version constraint for ansible-core.
type AnsibleEngine struct {
	Core string `json:"core,omitempty"`
}

// BindingOverride binds aspects of matching resources to other engines.
type BindingOverride struct {
	Selector Selector          `json:"selector"`
	Bindings map[string]string `json:"bindings"`
}

// Selector selects resources by label.
type Selector struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// StateUnits configures how engine code is split into state units.
type StateUnits struct {
	Strategy string `json:"strategy,omitempty"`
}

// Git configures mirroring of the workspace repository.
type Git struct {
	Mirror *GitMirror `json:"mirror,omitempty"`
}

// GitMirror is an external Git remote the workspace is mirrored to or from.
type GitMirror struct {
	URL       string `json:"url"`
	Direction string `json:"direction,omitempty"`
}

// ProxmoxClusterSpec is the spec of a ProxmoxCluster.
type ProxmoxClusterSpec struct {
	Endpoints      []string        `json:"endpoints"`
	CredentialsRef string          `json:"credentialsRef"`
	TLS            *ProxmoxTLS     `json:"tls,omitempty"`
	Nodes          []string        `json:"nodes,omitempty"`
	Network        *ProxmoxNetwork `json:"network,omitempty"`
	MacPrefix      string          `json:"macPrefix,omitempty"`
}

// ProxmoxTLS holds the TLS settings of a cluster.
type ProxmoxTLS struct {
	Fingerprint string `json:"fingerprint,omitempty"`
}

// ProxmoxNetwork holds the network settings of a cluster.
type ProxmoxNetwork struct {
	GuestBridge string `json:"guestBridge,omitempty"`
}

// DefaultGuestBridge is the bridge that guest network interfaces attach to
// unless the cluster names another one.
const DefaultGuestBridge = "vmbr0"

// DefaultMACPrefix is the three-octet prefix nodr uses for allocated MAC
// addresses unless the cluster names another one.
const DefaultMACPrefix = "BC:24:11"

// GuestBridge returns the bridge that guest network interfaces attach to.
func (s *ProxmoxClusterSpec) GuestBridge() string {
	if s.Network != nil && s.Network.GuestBridge != "" {
		return s.Network.GuestBridge
	}
	return DefaultGuestBridge
}

// MACPrefix returns the prefix nodr uses for allocated MAC addresses.
func (s *ProxmoxClusterSpec) MACPrefix() string {
	if s.MacPrefix != "" {
		return s.MacPrefix
	}
	return DefaultMACPrefix
}

// NetworkSpec is the spec of a Network.
type NetworkSpec struct {
	// VLAN is the VLAN ID, or 0 for an untagged network.
	VLAN      int          `json:"vlan,omitempty"`
	Zone      string       `json:"zone,omitempty"`
	IPv4      *NetworkIPv4 `json:"ipv4,omitempty"`
	DNS       *NetworkDNS  `json:"dns,omitempty"`
	Wireless  *Wireless    `json:"wireless,omitempty"`
	RealizeOn *RealizeOn   `json:"realizeOn,omitempty"`
}

// NetworkIPv4 holds the IPv4 settings of a network.
type NetworkIPv4 struct {
	Subnet  string       `json:"subnet"`
	Gateway string       `json:"gateway,omitempty"`
	DHCP    *DHCP        `json:"dhcp,omitempty"`
	Static  *StaticRange `json:"static,omitempty"`
}

// DHCP holds the DHCP settings of a network.
type DHCP struct {
	Range     string `json:"range,omitempty"`
	LeaseTime string `json:"leaseTime,omitempty"`
}

// StaticRange is the range nodr allocates static addresses from.
type StaticRange struct {
	Range string `json:"range,omitempty"`
}

// NetworkDNS holds the DNS settings of a network.
type NetworkDNS struct {
	Domain string `json:"domain,omitempty"`
}

// Wireless describes the Wi-Fi network bound to a network.
type Wireless struct {
	SSID          string `json:"ssid"`
	Security      string `json:"security"`
	PassphraseRef string `json:"passphraseRef,omitempty"`
}

// RealizeOn lists the systems a network is configured on.
type RealizeOn struct {
	Routers  []string `json:"routers,omitempty"`
	Clusters []string `json:"clusters,omitempty"`
}

// TemplateSpec is the spec of a Template.
type TemplateSpec struct {
	Cluster  string        `json:"cluster"`
	Node     string        `json:"node,omitempty"`
	Image    TemplateImage `json:"image"`
	OS       *TemplateOS   `json:"os,omitempty"`
	Storage  string        `json:"storage"`
	Identity Identity      `json:"identity,omitzero"`
}

// TemplateImage is the cloud image a template is built from.
type TemplateImage struct {
	URL      string `json:"url"`
	Checksum string `json:"checksum"`
}

// TemplateOS describes the operating system of a template.
type TemplateOS struct {
	Family  string `json:"family,omitempty"`
	Version string `json:"version,omitempty"`
}

// SSHKeySpec is the spec of an SSHKey.
type SSHKeySpec struct {
	PublicKey   string `json:"publicKey"`
	Description string `json:"description,omitempty"`
}

// Power states of a VirtualMachine.
const (
	PowerStateRunning   = "running"
	PowerStateStopped   = "stopped"
	PowerStateUnmanaged = "unmanaged"
)

// IPv4 addressing modes of a network interface.
const (
	IPv4ModeAuto   = "auto"
	IPv4ModeStatic = "static"
	IPv4ModeDHCP   = "dhcp"
)

// Defaults of VirtualMachine fields, as declared in the schema.
const (
	DefaultNode        = "auto"
	DefaultCPUType     = "x86-64-v2-AES"
	DefaultSockets     = 1
	DefaultMaxRestart  = 1
	DefaultMaxRelocate = 1
)

// VirtualMachineSpec is the spec of a VirtualMachine.
type VirtualMachineSpec struct {
	Placement Placement       `json:"placement"`
	Identity  Identity        `json:"identity,omitzero"`
	Source    Source          `json:"source,omitzero"`
	Resources Resources       `json:"resources"`
	Disks     []Disk          `json:"disks,omitempty"`
	NICs      []NIC           `json:"nics,omitempty"`
	Guest     Guest           `json:"guest,omitzero"`
	HA        *HA             `json:"ha,omitempty"`
	Policies  Policies        `json:"policies,omitzero"`
	Lifecycle Lifecycle       `json:"lifecycle,omitzero"`
	Proxmox   ProxmoxSettings `json:"proxmox,omitzero"`
}

// Placement says where a guest runs.
type Placement struct {
	Cluster      string   `json:"cluster"`
	Node         string   `json:"node,omitempty"`
	AssignedNode string   `json:"assignedNode,omitempty"`
	Affinity     Affinity `json:"affinity,omitzero"`
}

// Affinity constrains which guests share a node.
type Affinity struct {
	SeparateFrom []string `json:"separateFrom,omitempty"`
	KeepWith     []string `json:"keepWith,omitempty"`
}

// Identity holds allocated Proxmox identifiers.
type Identity struct {
	VMID int `json:"vmid,omitempty"`
}

// Source says what a guest is created from.
type Source struct {
	Template string `json:"template,omitempty"`
}

// Resources holds CPU and memory settings.
type Resources struct {
	CPU    CPU    `json:"cpu"`
	Memory Memory `json:"memory"`
}

// CPU holds CPU settings.
type CPU struct {
	Cores   int    `json:"cores"`
	Sockets int    `json:"sockets,omitempty"`
	Type    string `json:"type,omitempty"`
}

// Memory holds memory settings.
type Memory struct {
	Size    quantity.Quantity  `json:"size"`
	Minimum *quantity.Quantity `json:"minimum,omitempty"`
}

// Disk is a disk attached to a guest.
type Disk struct {
	Name    string            `json:"name"`
	Storage string            `json:"storage"`
	Size    quantity.Quantity `json:"size"`
	Options DiskOptions       `json:"options,omitzero"`
}

// DiskOptions holds optional disk settings.
type DiskOptions struct {
	Discard  bool `json:"discard,omitempty"`
	SSD      bool `json:"ssd,omitempty"`
	IOThread bool `json:"iothread,omitempty"`
}

// NIC is a network interface of a guest.
type NIC struct {
	Network string `json:"network"`
	MAC     string `json:"mac,omitempty"`
	IPv4    *IPv4  `json:"ipv4,omitempty"`
}

// IPv4 holds the IPv4 addressing of a network interface.
type IPv4 struct {
	Mode    string `json:"mode,omitempty"`
	Address string `json:"address,omitempty"`
}

// Guest holds settings inside the guest operating system.
type Guest struct {
	Agent     *bool      `json:"agent,omitempty"`
	CloudInit *CloudInit `json:"cloudInit,omitempty"`
	Profile   string     `json:"profile,omitempty"`
}

// CloudInit holds cloud-init settings.
type CloudInit struct {
	User           string   `json:"user,omitempty"`
	AuthorizedKeys []string `json:"authorizedKeys,omitempty"`
}

// HA holds high-availability settings.
type HA struct {
	Enabled     bool `json:"enabled,omitempty"`
	MaxRestart  *int `json:"maxRestart,omitempty"`
	MaxRelocate *int `json:"maxRelocate,omitempty"`
}

// Policies references the automation policies of a guest.
type Policies struct {
	Backup  string `json:"backup,omitempty"`
	Updates string `json:"updates,omitempty"`
}

// Lifecycle holds lifecycle settings.
type Lifecycle struct {
	PowerState  string   `json:"powerState,omitempty"`
	Protection  *bool    `json:"protection,omitempty"`
	StartOnBoot *bool    `json:"startOnBoot,omitempty"`
	IgnoreDrift []string `json:"ignoreDrift,omitempty"`
}

// ProxmoxSettings holds settings that only exist on Proxmox VE.
type ProxmoxSettings struct {
	Machine        string   `json:"machine,omitempty"`
	BIOS           string   `json:"bios,omitempty"`
	SCSIController string   `json:"scsiController,omitempty"`
	Tags           []string `json:"tags,omitempty"`
}

// DeepCopy returns a copy of the spec that shares no memory with s.
func (s VirtualMachineSpec) DeepCopy() VirtualMachineSpec {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("v1alpha1: copy VirtualMachineSpec: %v", err))
	}
	var out VirtualMachineSpec
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(fmt.Sprintf("v1alpha1: copy VirtualMachineSpec: %v", err))
	}
	return out
}

// WithDefaults returns a copy of the spec with the schema defaults filled
// in for fields that are not set.
func (s VirtualMachineSpec) WithDefaults() VirtualMachineSpec {
	out := s.DeepCopy()
	if out.Placement.Node == "" {
		out.Placement.Node = DefaultNode
	}
	if out.Resources.CPU.Sockets == 0 {
		out.Resources.CPU.Sockets = DefaultSockets
	}
	if out.Resources.CPU.Type == "" {
		out.Resources.CPU.Type = DefaultCPUType
	}
	if out.Guest.Agent == nil {
		out.Guest.Agent = ptr(true)
	}
	if out.Lifecycle.PowerState == "" {
		out.Lifecycle.PowerState = PowerStateRunning
	}
	if out.Lifecycle.Protection == nil {
		out.Lifecycle.Protection = ptr(true)
	}
	if out.Lifecycle.StartOnBoot == nil {
		out.Lifecycle.StartOnBoot = ptr(true)
	}
	for i := range out.NICs {
		if out.NICs[i].IPv4 == nil {
			out.NICs[i].IPv4 = &IPv4{}
		}
		if out.NICs[i].IPv4.Mode == "" {
			out.NICs[i].IPv4.Mode = IPv4ModeDHCP
		}
	}
	if out.HA != nil {
		if out.HA.MaxRestart == nil {
			out.HA.MaxRestart = ptr(DefaultMaxRestart)
		}
		if out.HA.MaxRelocate == nil {
			out.HA.MaxRelocate = ptr(DefaultMaxRelocate)
		}
	}
	return out
}

func ptr[T any](v T) *T { return &v }
