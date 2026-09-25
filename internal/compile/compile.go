// Package compile writes intent into engine code: it renders the
// VirtualMachines of a workspace into the OpenTofu state units below
// terraform/ (design §5.3 and §5.5). It works in that direction only; code
// edits are not lifted into intent here.
package compile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/lens/hclmap"
	"github.com/centopw/nodr/internal/lens/proxmoxvm"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/resolve"
	"github.com/centopw/nodr/internal/workspace"
)

// Compile writes the VirtualMachines of ws into the OpenTofu state units
// below terraform/ and returns the files that change, by workspace-relative
// path, with their new content. It writes nothing itself, so callers can
// show the changes before they call Write.
//
// VMs are compiled in name order. The managed block of a VM, the
// proxmox_virtual_environment_vm resource with a provenance comment that
// names the VM, is updated in place by the lens's Put, which changes only
// synced values, so code-owned values, extensions and hand edits stay as
// they are. A resource without that comment is never changed, even if it
// has the address of a VM. A VM without a managed block gets a new one at
// the end of terraform/<cluster>-compute/vms.tf, unless that unit has a
// resource with the VM's address already. Every unit directory with managed
// blocks gets versions.tf and providers.tf if it lacks them; files that
// exist are never overwritten. A managed block whose VM is not in intent
// stays and gets a warning.
//
// A VM that cannot be written, because it is not admitted or because
// intent would remove code that nodr does not own, gets an error and is
// skipped; the files still hold the changes for the other VMs. If a .tf
// file does not parse, Compile returns no files, since that file may hold
// managed blocks. The workspace must be valid. The result depends only on
// the workspace, and a workspace whose code agrees with its intent gives
// no files.
func Compile(ws *workspace.Workspace) (map[string][]byte, diag.List) {
	c := &compiler{
		ws:     ws,
		lens:   proxmoxvm.Lens{Resolver: resolve.NewIndex(ws.Documents)},
		disk:   map[string][]byte{},
		files:  map[string][]byte{},
		blocks: map[string][]proxmoxvm.ManagedBlock{},
		units:  map[string]map[string]bool{},
	}
	if c.scan() {
		for _, vm := range c.virtualMachines() {
			c.compileVM(vm)
		}
		c.warnOrphans()
		c.addUnitFiles()
	}
	c.diags.Sort()
	return c.changes(), c.diags
}

// compiler holds the state of one compilation.
type compiler struct {
	ws   *workspace.Workspace
	lens proxmoxvm.Lens
	// disk holds the .tf files below terraform/ as they are on disk, and
	// files the content they get, together with new files.
	disk, files map[string][]byte
	// blocks holds the managed blocks of each .tf file on disk.
	blocks map[string][]proxmoxvm.ManagedBlock
	// units holds, for each unit directory with managed blocks, the
	// clusters of their VMs.
	units map[string]map[string]bool
	diags diag.List
}

// scan reads the .tf files below terraform/ and finds the managed blocks
// in them. It reports whether every file parses.
func (c *compiler) scan() bool {
	if _, err := fs.Stat(c.ws.FS, terraformDir); errors.Is(err, fs.ErrNotExist) {
		return true
	}
	ok := true
	err := fs.WalkDir(c.ws.FS, terraformDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Like OpenTofu, skip hidden files, such as the lock files of
		// editors, and hidden directories, such as .terraform, where
		// OpenTofu keeps the modules it downloads.
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || path.Ext(p) != ".tf" {
			return nil
		}
		src, err := fs.ReadFile(c.ws.FS, p)
		if err != nil {
			return err
		}
		c.disk[p], c.files[p] = src, src
		blocks, err := proxmoxvm.ManagedBlocks(src, p)
		if err != nil {
			c.syntaxErrors(p, err)
			ok = false
		}
		c.blocks[p] = blocks
		return nil
	})
	if err != nil {
		c.diags.Errorf(terraformDir, 0, "", "read the engine code: %v", err)
		return false
	}
	return ok
}

// syntaxErrors reports why a .tf file does not parse.
func (c *compiler) syntaxErrors(file string, err error) {
	var hclDiags hcl.Diagnostics
	if !errors.As(err, &hclDiags) {
		c.diags.Errorf(file, 0, "", "%v", err)
		return
	}
	for _, d := range hclDiags {
		if d.Severity != hcl.DiagError {
			continue
		}
		line, msg := 0, d.Summary
		if d.Subject != nil {
			line = d.Subject.Start.Line
		}
		if d.Detail != "" {
			msg += "; " + d.Detail
		}
		c.diags.Errorf(file, line, "", "invalid HCL: %s", msg)
	}
}

// virtualMachines returns the VirtualMachines of the workspace in name
// order.
func (c *compiler) virtualMachines() []*v1alpha1.VirtualMachine {
	docs := c.ws.OfKind(v1alpha1.KindVirtualMachine)
	sort.Slice(docs, func(i, j int) bool { return docs[i].Metadata.Name < docs[j].Metadata.Name })
	var vms []*v1alpha1.VirtualMachine
	for _, d := range docs {
		vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](d)
		if err != nil {
			c.diags.Errorf(d.File, d.Line, "", "%v", err)
			continue
		}
		vms = append(vms, vm)
	}
	return vms
}

// compileVM writes vm into its managed block, or appends a new managed
// block to the unit of its cluster.
func (c *compiler) compileVM(vm *v1alpha1.VirtualMachine) {
	name, cluster := vm.Metadata.Name, vm.Spec.Placement.Cluster
	file, ok := c.managedFile(name)
	if !ok {
		dir := unitDir(cluster)
		if other := c.unmanagedBlock(dir, name); other != "" {
			c.diags.Errorf(other, 0, "", "cannot add a managed block for vm/%s: the unit has a resource %s.%s that nodr does not manage; add the comment '# %s' above it to let nodr manage it, or rename it", name, proxmoxvm.ResourceType, proxmoxvm.Address(name), proxmoxvm.Marker(name))
			return
		}
		block, err := c.lens.Render(vm)
		if err != nil {
			c.lensError(vm, "", err)
			return
		}
		file = path.Join(dir, vmsFile)
		c.files[file] = appendBlock(c.files[file], block)
		c.addUnit(dir, cluster)
		return
	}
	c.addUnit(path.Dir(file), cluster)
	// A VM compiled earlier may have changed the file already.
	out, err := c.lens.Put(vm, c.files[file], file)
	if err != nil {
		c.lensError(vm, file, err)
		return
	}
	c.files[file] = out
}

// managedFile returns the first file, in path order, with the managed block
// of the VM called name. Only a block with a provenance comment is managed:
// a block that merely has the VM's address is code that nodr does not own,
// wherever it is.
func (c *compiler) managedFile(name string) (string, bool) {
	for _, file := range slices.Sorted(maps.Keys(c.blocks)) {
		for _, b := range c.blocks[file] {
			if b.Name == name {
				return file, true
			}
		}
	}
	return "", false
}

// unmanagedBlock returns the file right in the unit in dir that has a
// resource with the address of the VM called name, or "" if there is none.
// The VM has no managed block, so such a resource is code that nodr does not
// own, and a new block with the same address would clash with it.
func (c *compiler) unmanagedBlock(dir, name string) string {
	for _, file := range slices.Sorted(maps.Keys(c.files)) {
		if path.Dir(file) != dir {
			continue
		}
		if _, err := proxmoxvm.Locate(c.files[file], file, name); err == nil {
			return file
		}
	}
	return ""
}

// lensError reports why the lens cannot write vm. file is the file with the
// managed block of the VM, or "" if it has none.
func (c *compiler) lensError(vm *v1alpha1.VirtualMachine, file string, err error) {
	d, name := vm.Document, vm.Metadata.Name
	var conflict *hclmap.ConflictError
	switch {
	case errors.Is(err, proxmoxvm.ErrNotAdmitted):
		c.diags.Errorf(d.File, d.Line, "", "%v; run 'nodr admit vm/%s' first", err, name)
	case errors.As(err, &conflict):
		c.diags.Errorf(file, c.line(file, name), "", "cannot update the managed block of vm/%s: intent removes blocks that hold code-owned values or extensions (%s); delete them by hand or keep them in intent", name, strings.Join(conflict.Paths, ", "))
	default:
		c.diags.Errorf(d.File, d.Line, "", "%v", err)
	}
}

// line returns the line of the managed block of the VM called name in
// file, as the file is on disk, or 0 if the block has no provenance
// comment.
func (c *compiler) line(file, name string) int {
	for _, b := range c.blocks[file] {
		if b.Name == name {
			return b.Line
		}
	}
	return 0
}

// warnOrphans reports managed blocks whose VM is not in intent. They stay,
// because without the block OpenTofu deletes the VM, and nodr cannot tell
// whether that is what the user wants.
func (c *compiler) warnOrphans() {
	for _, file := range slices.Sorted(maps.Keys(c.blocks)) {
		for _, b := range c.blocks[file] {
			if c.ws.Find(nrm.Ref{Kind: v1alpha1.KindVirtualMachine, Name: b.Name}) == nil {
				c.diags.Warnf(file, b.Line, "", "vm/%s does not exist in intent; delete its managed block to delete the VM, or remove the block's nodr:managed comment to keep the VM as code you own", b.Name)
			}
		}
	}
}

// exists reports whether the file at p exists on disk or was created.
func (c *compiler) exists(p string) bool {
	_, ok := c.files[p]
	return ok
}

// changes returns the files whose content differs from the disk.
func (c *compiler) changes() map[string][]byte {
	out := map[string][]byte{}
	for p, content := range c.files {
		if old, ok := c.disk[p]; !ok || !bytes.Equal(old, content) {
			out[p] = content
		}
	}
	return out
}

// appendBlock returns src with block appended, separated from the content
// before it by one blank line.
func appendBlock(src, block []byte) []byte {
	head := bytes.TrimRight(src, " \t\r\n")
	if len(head) == 0 {
		return block
	}
	out := make([]byte, 0, len(head)+2+len(block))
	out = append(out, head...)
	out = append(out, "\n\n"...)
	return append(out, block...)
}

// Write writes files, as Compile returns them, into the workspace and
// creates the directories they need. It checks every path before it writes
// any file.
func Write(ws *workspace.Workspace, files map[string][]byte) error {
	paths := slices.Sorted(maps.Keys(files))
	for _, p := range paths {
		if !filepath.IsLocal(filepath.FromSlash(p)) {
			return fmt.Errorf("cannot write %q: the path is not inside the workspace", p)
		}
	}
	for _, p := range paths {
		name := filepath.Join(ws.Root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(name, files[p], 0o644); err != nil {
			return err
		}
	}
	return nil
}
