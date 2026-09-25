// Package resolve answers the questions lenses ask about other resources in
// a workspace, in both directions: a network name to a bridge and VLAN and
// back, a template name to a guest ID and node and back, SSH key names to
// public keys and back. It is the reference-resolution stage of the intent
// compiler (design §5.3).
package resolve

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
)

// Errors that resolving a reference can wrap.
var (
	// ErrNotFound reports a reference to a resource that does not exist.
	ErrNotFound = errors.New("not found")
	// ErrNotAllocated reports a resource whose identity, such as a guest ID,
	// has not been allocated yet.
	ErrNotAllocated = errors.New("not allocated yet")
)

// Network describes how a network appears to guests of a cluster.
type Network struct {
	Name string
	// Bridge is the bridge guest interfaces attach to.
	Bridge string
	// VLAN is the VLAN tag, or 0 for an untagged network.
	VLAN int
	// Gateway is the IPv4 gateway, or empty.
	Gateway string
}

// Template describes a VM template of a cluster.
type Template struct {
	Name string
	// VMID is the guest ID of the template.
	VMID int
	// Node is the node that holds the template, or empty if the template
	// does not say.
	Node string
}

// Resolver resolves references between resources.
type Resolver interface {
	// Network returns the network with the given name as guests of the
	// cluster see it.
	Network(cluster, name string) (Network, error)
	// NetworkFor returns the network that guests of the cluster reach
	// through bridge with the given VLAN tag (0 for untagged).
	NetworkFor(cluster, bridge string, vlan int) (Network, error)
	// Template returns the template with the given name, which has to
	// belong to the cluster.
	Template(cluster, name string) (Template, error)
	// TemplateFor returns the template of the cluster with a guest ID.
	TemplateFor(cluster string, vmid int) (Template, error)
	// PublicKeys returns the public keys of SSH keys, in the same order.
	PublicKeys(names []string) ([]string, error)
	// KeyNames returns the names of the SSH keys with the given public
	// keys, in the same order. Key comments are ignored.
	KeyNames(keys []string) ([]string, error)
}

// Index is a Resolver built from the intent documents of a workspace.
type Index struct {
	clusters  map[string]*v1alpha1.ProxmoxCluster
	networks  []*v1alpha1.Network
	templates []*v1alpha1.Template
	keys      map[string]string   // name -> public key
	keyNames  map[string][]string // normalized public key -> names
}

var _ Resolver = (*Index)(nil)

// NewIndex builds an index from intent documents. Documents that do not
// decode are skipped; validation reports them.
func NewIndex(docs []*nrm.Document) *Index {
	ix := &Index{
		clusters: map[string]*v1alpha1.ProxmoxCluster{},
		keys:     map[string]string{},
		keyNames: map[string][]string{},
	}
	for _, d := range docs {
		if d.APIVersion != v1alpha1.APIVersion {
			continue
		}
		switch d.Kind {
		case v1alpha1.KindProxmoxCluster:
			if c, err := v1alpha1.Decode[v1alpha1.ProxmoxClusterSpec](d); err == nil {
				ix.clusters[c.Metadata.Name] = c
			}
		case v1alpha1.KindNetwork:
			if n, err := v1alpha1.Decode[v1alpha1.NetworkSpec](d); err == nil {
				ix.networks = append(ix.networks, n)
			}
		case v1alpha1.KindTemplate:
			if t, err := v1alpha1.Decode[v1alpha1.TemplateSpec](d); err == nil {
				ix.templates = append(ix.templates, t)
			}
		case v1alpha1.KindSSHKey:
			if k, err := v1alpha1.Decode[v1alpha1.SSHKeySpec](d); err == nil {
				ix.keys[k.Metadata.Name] = k.Spec.PublicKey
				norm := normalizeKey(k.Spec.PublicKey)
				ix.keyNames[norm] = append(ix.keyNames[norm], k.Metadata.Name)
			}
		}
	}
	sort.Slice(ix.networks, func(i, j int) bool { return ix.networks[i].Metadata.Name < ix.networks[j].Metadata.Name })
	sort.Slice(ix.templates, func(i, j int) bool { return ix.templates[i].Metadata.Name < ix.templates[j].Metadata.Name })
	return ix
}

func (ix *Index) guestBridge(cluster string) (string, error) {
	c, ok := ix.clusters[cluster]
	if !ok {
		return "", fmt.Errorf("cluster %q: %w", cluster, ErrNotFound)
	}
	return c.Spec.GuestBridge(), nil
}

// realizedOn reports whether a network is carried by a cluster. A network
// without an explicit list of clusters is carried everywhere.
func realizedOn(n *v1alpha1.Network, cluster string) bool {
	ro := n.Spec.RealizeOn
	return ro == nil || len(ro.Clusters) == 0 || slices.Contains(ro.Clusters, cluster)
}

func toNetwork(n *v1alpha1.Network, bridge string) Network {
	out := Network{Name: n.Metadata.Name, Bridge: bridge, VLAN: n.Spec.VLAN}
	if n.Spec.IPv4 != nil {
		out.Gateway = n.Spec.IPv4.Gateway
	}
	return out
}

// Network implements Resolver.
func (ix *Index) Network(cluster, name string) (Network, error) {
	bridge, err := ix.guestBridge(cluster)
	if err != nil {
		return Network{}, err
	}
	for _, n := range ix.networks {
		if n.Metadata.Name != name {
			continue
		}
		if !realizedOn(n, cluster) {
			return Network{}, fmt.Errorf("network %q is not realized on cluster %q", name, cluster)
		}
		return toNetwork(n, bridge), nil
	}
	return Network{}, fmt.Errorf("network %q: %w", name, ErrNotFound)
}

// NetworkFor implements Resolver.
func (ix *Index) NetworkFor(cluster, bridge string, vlan int) (Network, error) {
	guestBridge, err := ix.guestBridge(cluster)
	if err != nil {
		return Network{}, err
	}
	if bridge != guestBridge {
		return Network{}, fmt.Errorf("bridge %q is not the guest bridge %q of cluster %q", bridge, guestBridge, cluster)
	}
	var matches []*v1alpha1.Network
	for _, n := range ix.networks {
		if n.Spec.VLAN == vlan && realizedOn(n, cluster) {
			matches = append(matches, n)
		}
	}
	switch len(matches) {
	case 0:
		if vlan == 0 {
			return Network{}, fmt.Errorf("no untagged network on cluster %q: %w", cluster, ErrNotFound)
		}
		return Network{}, fmt.Errorf("no network with VLAN %d on cluster %q: %w", vlan, cluster, ErrNotFound)
	case 1:
		return toNetwork(matches[0], guestBridge), nil
	default:
		names := make([]string, len(matches))
		for i, n := range matches {
			names[i] = n.Metadata.Name
		}
		return Network{}, fmt.Errorf("VLAN %d on cluster %q is ambiguous: networks %s", vlan, cluster, strings.Join(names, ", "))
	}
}

func toTemplate(t *v1alpha1.Template) Template {
	return Template{Name: t.Metadata.Name, VMID: t.Spec.Identity.VMID, Node: t.Spec.Node}
}

// Template implements Resolver.
func (ix *Index) Template(cluster, name string) (Template, error) {
	for _, t := range ix.templates {
		if t.Metadata.Name != name {
			continue
		}
		if t.Spec.Cluster != cluster {
			return Template{}, fmt.Errorf("template %q belongs to cluster %q, not %q", name, t.Spec.Cluster, cluster)
		}
		if t.Spec.Identity.VMID == 0 {
			return Template{}, fmt.Errorf("the guest ID of template %q is %w", name, ErrNotAllocated)
		}
		return toTemplate(t), nil
	}
	return Template{}, fmt.Errorf("template %q: %w", name, ErrNotFound)
}

// TemplateFor implements Resolver.
func (ix *Index) TemplateFor(cluster string, vmid int) (Template, error) {
	var matches []*v1alpha1.Template
	for _, t := range ix.templates {
		if vmid != 0 && t.Spec.Identity.VMID == vmid && t.Spec.Cluster == cluster {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return Template{}, fmt.Errorf("no template with guest ID %d on cluster %q: %w", vmid, cluster, ErrNotFound)
	case 1:
		return toTemplate(matches[0]), nil
	default:
		names := make([]string, len(matches))
		for i, t := range matches {
			names[i] = t.Metadata.Name
		}
		return Template{}, fmt.Errorf("guest ID %d on cluster %q is used by several templates: %s", vmid, cluster, strings.Join(names, ", "))
	}
}

// PublicKeys implements Resolver.
func (ix *Index) PublicKeys(names []string) ([]string, error) {
	out := make([]string, len(names))
	for i, name := range names {
		key, ok := ix.keys[name]
		if !ok {
			return nil, fmt.Errorf("SSH key %q: %w", name, ErrNotFound)
		}
		out[i] = key
	}
	return out, nil
}

// KeyNames implements Resolver.
func (ix *Index) KeyNames(keys []string) ([]string, error) {
	out := make([]string, len(keys))
	for i, key := range keys {
		names := ix.keyNames[normalizeKey(key)]
		switch len(names) {
		case 0:
			return nil, fmt.Errorf("no SSH key has the public key %s", shortKey(key))
		case 1:
			out[i] = names[0]
		default:
			return nil, fmt.Errorf("the public key %s belongs to several SSH keys: %s", shortKey(key), strings.Join(names, ", "))
		}
	}
	return out, nil
}

// normalizeKey drops the comment of an authorized_keys entry, keeping the
// key type and data.
func normalizeKey(key string) string {
	fields := strings.Fields(key)
	if len(fields) >= 2 {
		return fields[0] + " " + fields[1]
	}
	return strings.TrimSpace(key)
}

func shortKey(key string) string {
	k := normalizeKey(key)
	if len(k) > 40 {
		return k[:40] + "..."
	}
	return k
}
