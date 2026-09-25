package compile

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
)

// The provider that the code of VMs is written for (ADR-0005). versions.tf
// pins it in every state unit.
const (
	// ProviderSource is the source address of the provider.
	ProviderSource = "bpg/proxmox"
	// ProviderVersion constrains the provider versions that rendered code
	// works with.
	ProviderVersion = ">= 0.80, < 1.0"
	// providerName is the local name of the provider, which its resource
	// types start with.
	providerName = "proxmox"
)

// languageVersion constrains the OpenTofu version: rendered code uses the
// language of OpenTofu 1.8 and later (design §5.5).
const languageVersion = ">= 1.8"

// Layout of the OpenTofu code of a workspace: one directory per state unit
// below terraform/, with one file per kind (design §5.5).
const (
	terraformDir  = "terraform"
	vmsFile       = "vms.tf"
	versionsFile  = "versions.tf"
	providersFile = "providers.tf"
)

// unitDir returns the directory of the state unit that new managed blocks
// of a cluster's VMs go to, such as terraform/pve-main-compute.
func unitDir(cluster string) string {
	return path.Join(terraformDir, cluster+"-compute")
}

// addUnit records that the unit in dir holds the managed block of a VM of
// cluster.
func (c *compiler) addUnit(dir, cluster string) {
	if c.units[dir] == nil {
		c.units[dir] = map[string]bool{}
	}
	c.units[dir][cluster] = true
}

// addUnitFiles creates versions.tf and providers.tf in every unit that
// holds managed blocks but lacks them. A unit gets no versions.tf if one of
// its files declares the provider in required_providers already, and no
// providers.tf if one has a provider block for it, since OpenTofu rejects
// both twice. Files that exist stay as they are, whatever they contain.
func (c *compiler) addUnitFiles() {
	for _, dir := range slices.Sorted(maps.Keys(c.units)) {
		required, configured := c.declared(dir)
		if p := path.Join(dir, versionsFile); !c.exists(p) && !required {
			c.files[p] = versions()
		}
		if p := path.Join(dir, providersFile); !c.exists(p) && !configured {
			c.addProviders(p, slices.Sorted(maps.Keys(c.units[dir])))
		}
	}
}

// declared reports whether a .tf file of the unit in dir declares the
// provider in the required_providers block of a terraform block, and
// whether one has a provider block for it. Only the files right in dir
// count: those further down belong to modules.
func (c *compiler) declared(dir string) (required, configured bool) {
	for p, src := range c.files {
		if path.Dir(p) != dir {
			continue
		}
		// Every file parsed when it was read, and compiling keeps it so.
		file, diags := hclsyntax.ParseConfig(src, p, hcl.InitialPos)
		if diags.HasErrors() {
			continue
		}
		for _, b := range file.Body.(*hclsyntax.Body).Blocks {
			switch {
			case b.Type == "provider" && len(b.Labels) == 1 && b.Labels[0] == providerName:
				configured = true
			case b.Type == "terraform":
				for _, inner := range b.Body.Blocks {
					if _, ok := inner.Body.Attributes[providerName]; ok && inner.Type == "required_providers" {
						required = true
					}
				}
			}
		}
	}
	return required, configured
}

// addProviders creates providers.tf at p for a unit that holds VMs of the
// given clusters.
func (c *compiler) addProviders(p string, clusters []string) {
	if len(clusters) > 1 {
		c.diags.Errorf(p, 0, "", "cannot create the file: the unit holds VMs of the clusters %s, but its provider can reach only one; move the managed blocks of each cluster into a unit of its own", strings.Join(clusters, ", "))
		return
	}
	var cluster *v1alpha1.ProxmoxCluster
	if d := c.ws.Find(nrm.Ref{Kind: v1alpha1.KindProxmoxCluster, Name: clusters[0]}); d != nil {
		cluster, _ = v1alpha1.Decode[v1alpha1.ProxmoxClusterSpec](d)
	}
	if cluster == nil || len(cluster.Spec.Endpoints) == 0 {
		c.diags.Errorf(p, 0, "", "cannot create the file: cluster %q does not exist or has no endpoints", clusters[0])
		return
	}
	c.files[p] = providers(cluster)
}

// versions returns the content of versions.tf, which pins the provider.
func versions() []byte {
	return fmt.Appendf(nil, `# Managed by nodr.
terraform {
  required_version = %s

  required_providers {
    proxmox = {
      source  = %s
      version = %s
    }
  }
}
`, quote(languageVersion), quote(ProviderSource), quote(ProviderVersion))
}

// providers returns the content of providers.tf, which points the provider
// at the first endpoint of cluster, since it takes only one. The endpoint
// ends with a slash, as in the provider's documentation.
func providers(cluster *v1alpha1.ProxmoxCluster) []byte {
	endpoint := strings.TrimSuffix(cluster.Spec.Endpoints[0], "/") + "/"
	return fmt.Appendf(nil, `# Managed by nodr. The API token comes from the environment variable
# PROXMOX_VE_API_TOKEN, which nodr sets from the cluster's credentialsRef
# (%s) when it runs OpenTofu.
provider "proxmox" {
  endpoint = %s
}
`, cluster.Spec.CredentialsRef, quote(endpoint))
}

// quote returns s as an HCL string literal.
func quote(s string) string {
	return string(hclwrite.TokensForValue(cty.StringVal(s)).Bytes())
}
