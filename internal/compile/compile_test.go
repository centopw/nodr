package compile

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/lens/proxmoxvm"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/resolve"
	"github.com/centopw/nodr/internal/workspace"
)

// example is the workspace the tests start from. They only read it; every
// change goes to a copy in a temporary directory.
const example = "../../examples/homelab"

// Files of the example's state unit.
const (
	vmsPath       = "terraform/pve-main-compute/vms.tf"
	versionsPath  = "terraform/pve-main-compute/versions.tf"
	providersPath = "terraform/pve-main-compute/providers.tf"
)

// web02 is an admitted VM on the cluster of the example.
const web02 = `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-02
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D4
  labels: { nodr/environment: prod, app: website }
spec:
  placement: { cluster: pve-main, assignedNode: pve3 }
  identity: { vmid: 1013 }
  source: { template: debian-12-cloud }
  resources: { cpu: { cores: 2 }, memory: { size: 4Gi } }
  disks: [{ name: root, storage: ceph-vm, size: 32Gi }]
  nics: [{ network: dmz, ipv4: { mode: static, address: 10.0.20.22/24 } }]
`

// app01 is an admitted VM whose name comes before those of the example.
const app01 = `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: app-01
  labels: { nodr/environment: prod }
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 1014 }
  resources: { cpu: { cores: 1 }, memory: { size: 2Gi } }
`

// lab is a second cluster with an admitted VM.
const lab = `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata:
  name: pve-lab
spec:
  endpoints: [https://10.0.40.11:8006]
  credentialsRef: proxmox/pve-lab-token
  nodes: [lab1]
---
apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: lab-01
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D5
  labels: { nodr/environment: lab }
spec:
  placement: { cluster: pve-lab, assignedNode: lab1 }
  identity: { vmid: 2000 }
  resources: { cpu: { cores: 1 }, memory: { size: 1Gi } }
`

// copyExample copies the example into a temporary directory and returns
// the directory. Working files of OpenTofu, if any, are left out.
func copyExample(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	src := os.DirFS(example)
	err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == ".terraform":
			return fs.SkipDir
		case !d.Type().IsRegular():
			return nil
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		writeFile(t, root, p, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func removeFile(t *testing.T, root, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(name))); err != nil {
		t.Fatal(err)
	}
}

// edit replaces the first from in a file with to.
func edit(t *testing.T, root, name, from, to string) {
	t.Helper()
	content := readFile(t, root, name)
	if !strings.Contains(content, from) {
		t.Fatalf("%s does not contain %q", name, from)
	}
	writeFile(t, root, name, strings.Replace(content, from, to, 1))
}

// load loads the workspace at root and checks that it is valid.
func load(t *testing.T, root string) *workspace.Workspace {
	t.Helper()
	ws, diags := workspace.Load(root)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if diags := ws.Validate(reg); diags.HasErrors() {
		t.Fatalf("the workspace is invalid: %v", diags.Err())
	}
	return ws
}

// render returns the managed block that the lens renders for a VM.
func render(t *testing.T, ws *workspace.Workspace, name string) string {
	t.Helper()
	d := ws.Find(nrm.Ref{Kind: v1alpha1.KindVirtualMachine, Name: name})
	if d == nil {
		t.Fatalf("vm/%s does not exist", name)
	}
	vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](d)
	if err != nil {
		t.Fatal(err)
	}
	out, err := proxmoxvm.Lens{Resolver: resolve.NewIndex(ws.Documents)}.Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func checkDiags(t *testing.T, diags diag.List, want ...string) {
	t.Helper()
	got := make([]string, len(diags))
	for i, d := range diags {
		got[i] = d.String()
	}
	if !slices.Equal(got, want) {
		t.Errorf("diagnostics:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// checkFiles checks that exactly the files in want change, to the given
// content.
func checkFiles(t *testing.T, files map[string][]byte, want map[string]string) {
	t.Helper()
	for _, p := range slices.Sorted(maps.Keys(files)) {
		if w, ok := want[p]; !ok {
			t.Errorf("unexpected change of %s:\n%s", p, files[p])
		} else if string(files[p]) != w {
			t.Errorf("%s =\n%s\nwant\n%s", p, files[p], w)
		}
	}
	for _, p := range slices.Sorted(maps.Keys(want)) {
		if _, ok := files[p]; !ok {
			t.Errorf("%s did not change", p)
		}
	}
}

// checkInSync writes files into the workspace at root and checks that
// compiling it again changes nothing.
func checkInSync(t *testing.T, root string, ws *workspace.Workspace, files map[string][]byte) {
	t.Helper()
	if err := Write(ws, files); err != nil {
		t.Fatal(err)
	}
	for p, content := range files {
		if got := readFile(t, root, p); got != string(content) {
			t.Errorf("Write wrote %s as\n%s", p, got)
		}
	}
	again, diags := Compile(load(t, root))
	checkDiags(t, diags)
	checkFiles(t, again, nil)
}

func TestExampleIsInSync(t *testing.T) {
	files, diags := Compile(load(t, example))
	checkDiags(t, diags)
	checkFiles(t, files, nil)
}

func TestNewVMs(t *testing.T) {
	root := copyExample(t)
	writeFile(t, root, "intent/compute/web-02.yaml", web02)
	writeFile(t, root, "intent/lab/pve-lab.yaml", lab)
	ws := load(t, root)
	files, diags := Compile(ws)
	checkDiags(t, diags)
	if again, _ := Compile(ws); !reflect.DeepEqual(again, files) {
		t.Error("compiling the same workspace twice gave different files")
	}
	checkFiles(t, files, map[string]string{
		// web-02 goes after the other blocks of its cluster.
		vmsPath: readFile(t, root, vmsPath) + "\n" + render(t, ws, "web-02"),
		// lab-01 starts the unit of its cluster.
		"terraform/pve-lab-compute/vms.tf":      render(t, ws, "lab-01"),
		"terraform/pve-lab-compute/versions.tf": readFile(t, root, versionsPath),
		"terraform/pve-lab-compute/providers.tf": `# Managed by nodr. The API token comes from the environment variable
# PROXMOX_VE_API_TOKEN, which nodr sets from the cluster's credentialsRef
# (proxmox/pve-lab-token) when it runs OpenTofu.
provider "proxmox" {
  endpoint = "https://10.0.40.11:8006/"
}
`,
	})
	checkInSync(t, root, ws, files)
}

func TestChangedVM(t *testing.T) {
	root := copyExample(t)
	edit(t, root, "intent/compute/web-01.yaml", "memory: { size: 8Gi }", "memory: { size: 16Gi }")
	// A hand edit next to the value that changes. The block also has a
	// code-owned value, cores = var.web_cores, and an extension, smbios.
	edit(t, root, vmsPath, "  memory {\n", "  # Sized for the page cache.\n  memory {\n")
	code := readFile(t, root, vmsPath)
	ws := load(t, root)
	files, diags := Compile(ws)
	checkDiags(t, diags)
	checkFiles(t, files, map[string]string{
		vmsPath: strings.Replace(code, "dedicated = 8192", "dedicated = 16384", 1),
	})
	checkInSync(t, root, ws, files)
}

func TestSeveralVMsInOneFile(t *testing.T) {
	root := copyExample(t)
	writeFile(t, root, "intent/compute/web-02.yaml", web02)
	writeFile(t, root, "intent/compute/app-01.yaml", app01)
	edit(t, root, "intent/compute/dns-01.yaml", "memory: { size: 1Gi }", "memory: { size: 2Gi }")
	edit(t, root, "intent/compute/web-01.yaml", "memory: { size: 8Gi }", "memory: { size: 16Gi }")
	code := readFile(t, root, vmsPath)
	ws := load(t, root)
	files, diags := Compile(ws)
	checkDiags(t, diags)
	// Every VM sees the changes of the VMs before it in name order, and new
	// blocks are appended in that order.
	code = strings.NewReplacer("dedicated = 1024", "dedicated = 2048", "dedicated = 8192", "dedicated = 16384").Replace(code)
	checkFiles(t, files, map[string]string{
		vmsPath: code + "\n" + render(t, ws, "app-01") + "\n" + render(t, ws, "web-02"),
	})
	checkInSync(t, root, ws, files)
}

func TestUnitFiles(t *testing.T) {
	root := copyExample(t)
	removeFile(t, root, versionsPath)
	removeFile(t, root, providersPath)
	files, diags := Compile(load(t, root))
	checkDiags(t, diags)
	checkFiles(t, files, map[string]string{
		versionsPath:  readFile(t, example, versionsPath),
		providersPath: readFile(t, example, providersPath),
	})
}

func TestExistingUnitFilesStay(t *testing.T) {
	root := copyExample(t)
	edit(t, root, versionsPath, `">= 0.80, < 1.0"`, `"~> 0.85"`)
	edit(t, root, "intent/platform/pve-main.yaml", "https://10.0.10.11:8006", "https://10.0.10.14:8006")
	files, diags := Compile(load(t, root))
	checkDiags(t, diags)
	checkFiles(t, files, nil)
}

func TestNotAdmitted(t *testing.T) {
	root := copyExample(t)
	writeFile(t, root, "intent/compute/web-03.yaml", `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-03
  labels: { nodr/environment: prod }
spec:
  placement: { cluster: pve-main }
  resources: { cpu: { cores: 1 }, memory: { size: 1Gi } }
`)
	files, diags := Compile(load(t, root))
	checkDiags(t, diags, "intent/compute/web-03.yaml:1: error: render web-03: spec.placement.assignedNode is empty: not admitted yet; run 'nodr admit vm/web-03' first")
	checkFiles(t, files, nil)

	// A VM with a managed block keeps the block as it is.
	removeFile(t, root, "intent/compute/web-03.yaml")
	edit(t, root, "intent/compute/web-01.yaml", "  identity:\n    vmid: 1012\n", "")
	files, diags = Compile(load(t, root))
	checkDiags(t, diags, "intent/compute/web-01.yaml:2: error: put web-01: spec.identity.vmid is not set: not admitted yet; run 'nodr admit vm/web-01' first")
	checkFiles(t, files, nil)
}

func TestManagedBlockWithoutIntent(t *testing.T) {
	root := copyExample(t)
	removeFile(t, root, "intent/compute/dns-01.yaml")
	files, diags := Compile(load(t, root))
	checkDiags(t, diags, "terraform/pve-main-compute/vms.tf:2: warning: vm/dns-01 does not exist in intent; delete its managed block to delete the VM, or remove the block's nodr:managed comment to keep the VM as code you own")
	checkFiles(t, files, nil)
}

// TestUnmanagedBlockWithTheAddress checks that a resource without a
// provenance comment is not taken for the managed block of a VM, although it
// has the VM's address.
func TestUnmanagedBlockWithTheAddress(t *testing.T) {
	t.Run("in another unit", func(t *testing.T) {
		root := copyExample(t)
		// A unit written by hand, sorted before the unit of web-01.
		writeFile(t, root, "terraform/legacy/main.tf", `resource "proxmox_virtual_environment_vm" "web_01" {
  name      = "legacy-web"
  node_name = "pve9"
  vm_id     = 500
}
`)
		edit(t, root, "intent/compute/web-01.yaml", "memory: { size: 8Gi }", "memory: { size: 16Gi }")
		code := readFile(t, root, vmsPath)
		ws := load(t, root)
		files, diags := Compile(ws)
		checkDiags(t, diags)
		// Only the managed block changes, and the other unit gets no files.
		checkFiles(t, files, map[string]string{
			vmsPath: strings.Replace(code, "dedicated = 8192", "dedicated = 16384", 1),
		})
		checkInSync(t, root, ws, files)
	})

	t.Run("in the unit of the VM", func(t *testing.T) {
		root := copyExample(t)
		edit(t, root, vmsPath, "# nodr:managed vm/web-01\n", "")
		files, diags := Compile(load(t, root))
		checkDiags(t, diags, "terraform/pve-main-compute/vms.tf: error: cannot add a managed block for vm/web-01: the unit has a resource proxmox_virtual_environment_vm.web_01 that nodr does not manage; add the comment '# nodr:managed vm/web-01' above it to let nodr manage it, or rename it")
		checkFiles(t, files, nil)
	})
}

func TestConflict(t *testing.T) {
	root := copyExample(t)
	// A second disk of dns-01, added in code with an attribute that nodr
	// does not model. Intent has one disk, so Put would have to remove the
	// block and the attribute with it.
	edit(t, root, vmsPath, "    discard      = \"ignore\"\n  }\n", `    discard      = "ignore"
  }

  disk {
    datastore_id = "local-lvm"
    interface    = "scsi1"
    size         = 16
    discard      = "ignore"
    backup       = false
  }
`)
	edit(t, root, "intent/compute/web-01.yaml", "memory: { size: 8Gi }", "memory: { size: 16Gi }")
	code := readFile(t, root, vmsPath)
	files, diags := Compile(load(t, root))
	checkDiags(t, diags, "terraform/pve-main-compute/vms.tf:2: error: cannot update the managed block of vm/dns-01: intent removes blocks that hold code-owned values or extensions (disk[1]); delete them by hand or keep them in intent")
	// dns-01 is skipped, web-01 is not.
	checkFiles(t, files, map[string]string{
		vmsPath: strings.Replace(code, "dedicated = 8192", "dedicated = 16384", 1),
	})
}

func TestSyntaxError(t *testing.T) {
	root := copyExample(t)
	writeFile(t, root, "terraform/pve-main-compute/broken.tf", "variable \"x\" {\n  default =\n}\n")
	edit(t, root, "intent/compute/web-01.yaml", "memory: { size: 8Gi }", "memory: { size: 16Gi }")
	files, diags := Compile(load(t, root))
	// The broken file could hold managed blocks, so nothing changes.
	checkDiags(t, diags, "terraform/pve-main-compute/broken.tf:2: error: invalid HCL: Invalid expression; Expected the start of an expression, but found an invalid expression token.")
	checkFiles(t, files, nil)
}

// TestHiddenFiles checks that Compile skips what OpenTofu skips: hidden
// files, such as the lock files of editors, and hidden directories, such
// as .terraform, which holds the modules that OpenTofu downloads.
func TestHiddenFiles(t *testing.T) {
	root := copyExample(t)
	const invalid = "variable \"x\" {\n  default =\n}\n"
	writeFile(t, root, "terraform/pve-main-compute/.#vms.tf", invalid)
	writeFile(t, root, "terraform/pve-main-compute/.terraform/modules/net/main.tf", invalid)
	files, diags := Compile(load(t, root))
	checkDiags(t, diags)
	checkFiles(t, files, nil)
}

func TestUnitWithSeveralClusters(t *testing.T) {
	root := copyExample(t)
	writeFile(t, root, "intent/lab/pve-lab.yaml", lab)
	ws := load(t, root)
	// lab-01 was moved by hand into the unit of pve-main, which lacks
	// providers.tf.
	writeFile(t, root, vmsPath, readFile(t, root, vmsPath)+"\n"+render(t, ws, "lab-01"))
	removeFile(t, root, providersPath)
	files, diags := Compile(ws)
	checkDiags(t, diags, "terraform/pve-main-compute/providers.tf: error: cannot create the file: the unit holds VMs of the clusters pve-lab, pve-main, but its provider can reach only one; move the managed blocks of each cluster into a unit of its own")
	checkFiles(t, files, nil)
}

func TestWriteChecksPaths(t *testing.T) {
	root := t.TempDir()
	ws := &workspace.Workspace{Root: root}
	err := Write(ws, map[string][]byte{
		"terraform/unit/vms.tf": []byte("# nodr\n"),
		"../outside.tf":         []byte("# nodr\n"),
	})
	if err == nil {
		t.Error("Write accepted a path outside the workspace")
	}
	if _, err := os.Stat(filepath.Join(root, "terraform")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Write wrote files although a path was invalid: %v", err)
	}
}
