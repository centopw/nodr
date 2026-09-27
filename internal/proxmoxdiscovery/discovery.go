// Package proxmoxdiscovery reads the live QEMU guests of a Proxmox VE
// cluster and classifies them against a workspace's existing
// VirtualMachine intent (docs/design/06-proxmox.md §6.3, steps 1-2).
package proxmoxdiscovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/quantity"
	"github.com/centopw/nodr/internal/workspace"
)

// Status classifies one discovered guest against workspace intent.
type Status string

// Guest classifications.
const (
	// StatusManaged is a guest whose cluster and VMID match an existing
	// VirtualMachine document.
	StatusManaged Status = "managed"
	// StatusDiscovered is a guest with no matching intent and no sign
	// another tool manages it.
	StatusDiscovered Status = "discovered"
	// StatusOtherToolTagged is a guest with no matching intent whose tags
	// or description mention another infrastructure-as-code tool.
	StatusOtherToolTagged Status = "discovered (other tool?)"
)

// Guest is one live QEMU guest discovered on a cluster, classified against
// the workspace's existing intent.
type Guest struct {
	VMID        int
	Name        string
	Node        string
	Status      string // Proxmox power status: "running", "stopped"
	CPUs        int
	Memory      string // formatted with internal/quantity, e.g. "4Gi"
	Disk        string
	Tags        []string
	Description string
	Classified  Status
}

// otherTools are the substrings, matched case-insensitively against a
// guest's tags and description, that flag it as owned by another IaC tool
// (§6.3 step 2's own example: "tags or descriptions mention Terraform").
var otherTools = []string{"terraform", "ansible", "packer", "pulumi", "opentofu"}

// Discover reads the live QEMU guests of cluster from client, and
// classifies each against ws's existing VirtualMachine intent. It excludes
// templates (ClusterResource.Template == 1).
func Discover(ctx context.Context, client *proxmox.Client, ws *workspace.Workspace, cluster string) ([]Guest, error) {
	resources, err := client.GetClusterResources(ctx, "vm")
	if err != nil {
		return nil, fmt.Errorf("proxmoxdiscovery: list cluster resources: %w", err)
	}

	managed := managedVMIDs(ws, cluster)

	guests := make([]Guest, 0, len(resources))
	for _, r := range resources {
		if r.Type != "qemu" || r.Template == 1 {
			continue
		}
		tags, description, err := guestTags(ctx, client, r.Node, r.VMID)
		if err != nil {
			return nil, err
		}
		mem, err := quantity.FromUnits(r.MaxMem, 1)
		if err != nil {
			return nil, fmt.Errorf("proxmoxdiscovery: guest %d memory: %w", r.VMID, err)
		}
		disk, err := quantity.FromUnits(r.MaxDisk, 1)
		if err != nil {
			return nil, fmt.Errorf("proxmoxdiscovery: guest %d disk: %w", r.VMID, err)
		}
		g := Guest{
			VMID:        r.VMID,
			Name:        r.Name,
			Node:        r.Node,
			Status:      r.Status,
			CPUs:        r.MaxCPU,
			Memory:      mem.String(),
			Disk:        disk.String(),
			Tags:        tags,
			Description: description,
		}
		g.Classified = classify(g, managed)
		guests = append(guests, g)
	}
	return guests, nil
}

// managedVMIDs returns the VM IDs that ws already manages on cluster.
func managedVMIDs(ws *workspace.Workspace, cluster string) map[int]bool {
	managed := make(map[int]bool)
	for _, d := range ws.OfKind(v1alpha1.KindVirtualMachine) {
		vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](d)
		if err != nil {
			continue
		}
		if vm.Spec.Placement.Cluster == cluster {
			managed[vm.Spec.Identity.VMID] = true
		}
	}
	return managed
}

func classify(g Guest, managed map[int]bool) Status {
	if managed[g.VMID] {
		return StatusManaged
	}
	if mentionsOtherTool(g.Tags, g.Description) {
		return StatusOtherToolTagged
	}
	return StatusDiscovered
}

func mentionsOtherTool(tags []string, description string) bool {
	haystacks := append([]string{description}, tags...)
	for _, h := range haystacks {
		lower := strings.ToLower(h)
		for _, tool := range otherTools {
			if strings.Contains(lower, tool) {
				return true
			}
		}
	}
	return false
}

// guestTags fetches a guest's raw configuration and returns its Proxmox
// tags (semicolon-separated in the "tags" setting) and description.
func guestTags(ctx context.Context, client *proxmox.Client, node string, vmid int) (tags []string, description string, err error) {
	config, _, err := client.GetQEMUConfig(ctx, node, vmid)
	if err != nil {
		return nil, "", fmt.Errorf("proxmoxdiscovery: guest %d config: %w", vmid, err)
	}
	if raw, ok := config.RawSettings["tags"]; ok && raw != "" {
		tags = strings.Split(raw, ";")
	}
	description = config.RawSettings["description"]
	return tags, description, nil
}
