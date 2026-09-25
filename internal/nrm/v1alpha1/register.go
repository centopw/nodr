package v1alpha1

import (
	"embed"
	"fmt"
	"io/fs"
	"strconv"

	"github.com/centopw/nodr/internal/nrm"
)

//go:embed schemas/*.json
var schemaFiles embed.FS

// Schemas returns the JSON Schema files of this API version.
func Schemas() fs.FS {
	sub, err := fs.Sub(schemaFiles, "schemas")
	if err != nil {
		panic(fmt.Sprintf("v1alpha1: schemas: %v", err))
	}
	return sub
}

// Register adds the kinds of this API version to r.
func Register(r *nrm.Registry) error {
	if err := r.AddSchemas(APIVersion, Schemas()); err != nil {
		return err
	}
	kinds := []nrm.KindInfo{
		{Kind: KindWorkspace, Schema: "workspace.json", Manifest: true},
		{Kind: KindProxmoxCluster, Schema: "proxmoxcluster.json"},
		{Kind: KindNetwork, Schema: "network.json", References: networkReferences, Check: checkNetwork},
		{Kind: KindTemplate, Schema: "template.json", References: templateReferences, Check: checkTemplate},
		{Kind: KindSSHKey, Schema: "sshkey.json"},
		{Kind: KindVirtualMachine, ShortName: "vm", Schema: "virtualmachine.json", References: vmReferences, Check: checkVirtualMachine},
	}
	for _, k := range kinds {
		k.APIVersion = APIVersion
		if err := r.Register(k); err != nil {
			return err
		}
	}
	return nil
}

// NewRegistry returns a registry with the kinds of this API version.
func NewRegistry() (*nrm.Registry, error) {
	r := nrm.NewRegistry()
	if err := Register(r); err != nil {
		return nil, err
	}
	return r, nil
}

// refs collects references found in a document.
type refs []nrm.FieldRef

func (r *refs) add(kind, name string, path ...string) {
	if name != "" {
		*r = append(*r, nrm.FieldRef{Target: nrm.Ref{Kind: kind, Name: name}, Path: path})
	}
}

func (r *refs) addAll(kind string, names []string, path ...string) {
	for i, name := range names {
		r.add(kind, name, append(append([]string{}, path...), strconv.Itoa(i))...)
	}
}

func networkReferences(d *nrm.Document) ([]nrm.FieldRef, error) {
	n, err := Decode[NetworkSpec](d)
	if err != nil {
		return nil, err
	}
	var out refs
	if ro := n.Spec.RealizeOn; ro != nil {
		out.addAll(kindRouter, ro.Routers, "spec", "realizeOn", "routers")
		out.addAll(KindProxmoxCluster, ro.Clusters, "spec", "realizeOn", "clusters")
	}
	return out, nil
}

func templateReferences(d *nrm.Document) ([]nrm.FieldRef, error) {
	t, err := Decode[TemplateSpec](d)
	if err != nil {
		return nil, err
	}
	var out refs
	out.add(KindProxmoxCluster, t.Spec.Cluster, "spec", "cluster")
	return out, nil
}

func vmReferences(d *nrm.Document) ([]nrm.FieldRef, error) {
	vm, err := Decode[VirtualMachineSpec](d)
	if err != nil {
		return nil, err
	}
	s := vm.Spec
	var out refs
	out.add(KindProxmoxCluster, s.Placement.Cluster, "spec", "placement", "cluster")
	out.addAll(KindVirtualMachine, s.Placement.Affinity.SeparateFrom, "spec", "placement", "affinity", "separateFrom")
	out.addAll(KindVirtualMachine, s.Placement.Affinity.KeepWith, "spec", "placement", "affinity", "keepWith")
	out.add(KindTemplate, s.Source.Template, "spec", "source", "template")
	for i, nic := range s.NICs {
		out.add(KindNetwork, nic.Network, "spec", "nics", strconv.Itoa(i), "network")
	}
	if ci := s.Guest.CloudInit; ci != nil {
		out.addAll(KindSSHKey, ci.AuthorizedKeys, "spec", "guest", "cloudInit", "authorizedKeys")
	}
	out.add(kindConfigProfile, s.Guest.Profile, "spec", "guest", "profile")
	out.add(kindBackupPolicy, s.Policies.Backup, "spec", "policies", "backup")
	out.add(kindUpdatePolicy, s.Policies.Updates, "spec", "policies", "updates")
	return out, nil
}
