package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// example is the example workspace of the repository.
var example = filepath.Join("..", "..", "examples", "homelab")

type result struct {
	stdout, stderr string
	code           int
}

func run(args ...string) result {
	return runWithStdin(strings.NewReader(""), args...)
}

// runWithStdin runs the command line with stdin as its standard input.
func runWithStdin(stdin io.Reader, args ...string) result {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), nil, args, stdin, &stdout, &stderr)
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func (r result) check(t *testing.T, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.stdout, r.stderr)
	}
}

// writeWorkspace creates a workspace from file contents by path.
func writeWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const manifest = `apiVersion: nodr/v1alpha1
kind: Workspace
metadata:
  name: test
spec: {}
`

func TestVersion(t *testing.T) {
	r := run("version")
	r.check(t, exitOK)
	if !strings.HasPrefix(r.stdout, "nodr ") {
		t.Errorf("version = %q", r.stdout)
	}
}

func TestValidate(t *testing.T) {
	r := run("validate", "-w", example)
	r.check(t, exitOK)
	if r.stdout != "8 documents valid\n" || r.stderr != "" {
		t.Errorf("stdout = %q, stderr = %q", r.stdout, r.stderr)
	}
}

func TestValidateReportsProblems(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"nodr.yaml": manifest,
		"intent/network/lan.yaml": `apiVersion: nodr/v1alpha1
kind: Network
metadata:
  name: lan
spec:
  ipv4:
    subnet: 10.0.10.0/24
    gateway: 10.0.10.1
    dhcp: { range: 10.0.10.1-10.0.10.99 }
`,
	})
	r := run("validate", "-w", root)
	r.check(t, exitOK)
	if !strings.Contains(r.stderr, "intent/network/lan.yaml:9: warning: spec.ipv4.dhcp.range:") {
		t.Errorf("stderr = %q, want a warning with file, line and path", r.stderr)
	}
	if r.stdout != "1 document valid, with 1 warning\n" {
		t.Errorf("stdout = %q", r.stdout)
	}

	root = writeWorkspace(t, map[string]string{
		"nodr.yaml": manifest,
		"intent/compute/vm.yaml": `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: Web_01
spec:
  resources: { cpu: { cores: 0 } }
`,
	})
	r = run("validate", "-w", root)
	r.check(t, exitError)
	want := `intent/compute/vm.yaml:4: error: metadata.name: must contain only lowercase letters, digits and hyphens, and start and end with a letter or digit
intent/compute/vm.yaml:5: error: spec.placement: missing required field
intent/compute/vm.yaml:6: error: spec.resources.cpu.cores: minimum: got 0, want 1
intent/compute/vm.yaml:6: error: spec.resources.memory: missing required field
4 errors, 0 warnings in 1 document
`
	if r.stderr != want {
		t.Errorf("stderr =\n%s\nwant\n%s", r.stderr, want)
	}
}

func TestValidateReportsDuplicates(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"nodr.yaml": manifest,
		"intent/platform/pve.yaml": `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://10.0.10.11:8006]
  credentialsRef: proxmox/pve-main-token
---
apiVersion: nodr/v1alpha1
kind: Template
metadata: { name: debian }
spec:
  cluster: pve-main
  image: { url: https://example.com/debian.qcow2, checksum: "sha256:0000000000000000000000000000000000000000000000000000000000000000" }
  storage: local-lvm
  identity: { vmid: 9000 }
`,
		"intent/network/lan.yaml": `apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: lan }
spec:
  ipv4: { subnet: 10.0.10.0/24 }
`,
		"intent/compute/a.yaml": `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata: { name: a }
spec:
  placement: { cluster: pve-main }
  identity: { vmid: 1000 }
  resources: { cpu: { cores: 1 }, memory: { size: 1Gi } }
  nics:
    - network: lan
      mac: BC:24:11:00:00:01
      ipv4: { mode: static, address: 10.0.10.21/24 }
`,
		"intent/compute/b.yaml": `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata: { name: b }
spec:
  placement: { cluster: pve-main }
  identity: { vmid: 1000 }
  resources: { cpu: { cores: 1 }, memory: { size: 1Gi } }
  nics:
    - network: lan
      mac: bc:24:11:00:00:01
      ipv4: { mode: static, address: 10.0.10.21/25 }
    - network: lan
      ipv4: { mode: static, address: 10.0.10.11/24 }
`,
		"intent/compute/c.yaml": `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata: { name: c }
spec:
  placement: { cluster: pve-main }
  identity: { vmid: 9000 }
  resources: { cpu: { cores: 1 }, memory: { size: 1Gi } }
`,
	})
	r := run("validate", "-w", root)
	r.check(t, exitError)
	// Each value is reported where it is used again, in file order, so
	// the template in intent/platform comes after the VM that uses its
	// guest ID. An endpoint always comes first.
	want := `intent/compute/b.yaml:6: error: spec.identity.vmid: guest ID 1000 is used twice in cluster "pve-main"; it is also used by VirtualMachine/a at intent/compute/a.yaml:6
intent/compute/b.yaml:10: error: spec.nics[0].mac: MAC address bc:24:11:00:00:01 is used twice in cluster "pve-main"; it is also used by VirtualMachine/a at intent/compute/a.yaml:10
intent/compute/b.yaml:11: error: spec.nics[0].ipv4.address: address 10.0.10.21 is used twice in network "lan"; it is also used by VirtualMachine/a at intent/compute/a.yaml:11
intent/compute/b.yaml:13: error: spec.nics[1].ipv4.address: address 10.0.10.11 is used twice; it is also used by an endpoint of ProxmoxCluster/pve-main at intent/platform/pve.yaml:5
intent/platform/pve.yaml:15: error: spec.identity.vmid: guest ID 9000 is used twice in cluster "pve-main"; it is also used by VirtualMachine/c at intent/compute/c.yaml:6
5 errors, 0 warnings in 6 documents
`
	if r.stderr != want || r.stdout != "" {
		t.Errorf("stderr =\n%s\nwant\n%s\nstdout = %q", r.stderr, want, r.stdout)
	}
}

func TestValidateFindsTheWorkspace(t *testing.T) {
	root := writeWorkspace(t, map[string]string{"nodr.yaml": manifest, "intent/compute/.keep": ""})
	t.Chdir(filepath.Join(root, "intent", "compute"))
	r := run("validate")
	r.check(t, exitOK)
	if r.stdout != "0 documents valid\n" {
		t.Errorf("stdout = %q", r.stdout)
	}

	t.Chdir(t.TempDir())
	r = run("validate")
	r.check(t, exitError)
	if !strings.Contains(r.stderr, "not inside a nodr workspace") {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestRender(t *testing.T) {
	r := run("render", "-w", example, "vm/web-01")
	r.check(t, exitOK)
	for _, want := range []string{
		"# nodr:managed vm/web-01\nresource \"proxmox_virtual_environment_vm\" \"web_01\" {\n",
		"    cores   = 2\n",
		"    vm_id     = 9001\n    node_name = \"pve1\"\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("render lacks %q:\n%s", want, r.stdout)
		}
	}

	all := run("render", "-w", example)
	all.check(t, exitOK)
	dns, web := strings.Index(all.stdout, "vm/dns-01"), strings.Index(all.stdout, "vm/web-01")
	if dns < 0 || web < dns {
		t.Errorf("render of all VMs is not in name order:\n%s", all.stdout)
	}
	if !strings.HasSuffix(all.stdout, r.stdout) {
		t.Errorf("render of all VMs does not end with web-01")
	}
}

func TestRenderRefusesAnInvalidWorkspace(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"nodr.yaml":              manifest,
		"intent/compute/vm.yaml": "apiVersion: nodr/v1alpha1\nkind: VirtualMachine\nmetadata: { name: web-01 }\nspec: { size: 3 }\n",
	})
	r := run("render", "-w", root)
	r.check(t, exitError)
	if !strings.Contains(r.stderr, "unknown field") || !strings.Contains(r.stderr, "the workspace has errors") {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestDescribe(t *testing.T) {
	r := run("describe", "-w", example, "vm/web-01")
	r.check(t, exitOK)
	want := `Name:    web-01
Kind:    VirtualMachine
UID:     01J9Z3K4T7M2Q8V5X6N0B1C2D3
Intent:  intent/compute/web-01.yaml:2
Code:    terraform/pve-main-compute/vms.tf (proxmox_virtual_environment_vm web_01)
Fields:  29 synced, 1 code-owned, 1 extension
`
	if r.stdout != want {
		t.Errorf("describe =\n%s\nwant\n%s", r.stdout, want)
	}

	r = run("describe", "-w", example, "vm/web-01", "--ownership")
	r.check(t, exitOK)
	lines := strings.Split(r.stdout, "\n")
	var cores, smbios string
	for _, line := range lines {
		if strings.HasSuffix(line, " ") {
			t.Errorf("line with trailing spaces: %q", line)
		}
		switch {
		case strings.HasPrefix(line, "spec.resources.cpu.cores "):
			cores = strings.Join(strings.Fields(line), " ")
		case strings.HasPrefix(line, "smbios "):
			smbios = strings.Join(strings.Fields(line), " ")
		}
	}
	if cores != "spec.resources.cpu.cores code-owned terraform/pve-main-compute/vms.tf:85 set by an expression" {
		t.Errorf("cores row = %q", cores)
	}
	if smbios != "smbios extension terraform/pve-main-compute/vms.tf:127" {
		t.Errorf("smbios row = %q", smbios)
	}
}

func TestDescribeJSON(t *testing.T) {
	r := run("describe", "-w", example, "vm/web-01", "--ownership", "-o", "json")
	r.check(t, exitOK)
	var out struct {
		Name   string `json:"name"`
		Intent string `json:"intent"`
		Code   string `json:"code"`
		Fields []struct {
			Path     string `json:"path"`
			CodePath string `json:"codePath"`
			Owner    string `json:"owner"`
			Reason   string `json:"reason"`
			Line     int    `json:"line"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("%v\n%s", err, r.stdout)
	}
	if out.Name != "web-01" || out.Intent != "intent/compute/web-01.yaml" || out.Code != "terraform/pve-main-compute/vms.tf" {
		t.Errorf("describe = %+v", out)
	}
	found := false
	for _, f := range out.Fields {
		if f.Path == "spec.resources.cpu.cores" {
			found = true
			if f.CodePath != "cpu.cores" || f.Owner != "code-owned" || f.Line != 85 {
				t.Errorf("cores = %+v", f)
			}
		}
	}
	if !found {
		t.Errorf("no field spec.resources.cpu.cores in %+v", out.Fields)
	}
}

func TestDescribeWithoutCode(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"nodr.yaml": manifest,
		"intent/platform/pve.yaml": `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://10.0.10.11:8006]
  credentialsRef: proxmox/pve-main-token
`,
		"intent/compute/vm.yaml": `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-01
spec:
  placement: { cluster: pve-main }
  resources: { cpu: { cores: 2 }, memory: { size: 2Gi } }
`,
	})
	r := run("describe", "-w", root, "vm/web-01", "--ownership")
	r.check(t, exitOK)
	if !strings.Contains(r.stdout, "Code:    no managed block below terraform/\n") || strings.Contains(r.stdout, "FIELD") {
		t.Errorf("describe =\n%s", r.stdout)
	}
}

// admitFiles is a workspace with a VM that admission has to complete and
// one that only lacks its node.
var admitFiles = map[string]string{
	"nodr.yaml": `apiVersion: nodr/v1alpha1
kind: Workspace
metadata:
  name: test
spec:
  environments:
    prod: { vmidRange: [1000, 1999] }
    templates: { vmidRange: [9000, 9099] }
`,
	"intent/platform/pve.yaml": `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://10.0.10.11:8006]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve1, pve2]
`,
	"intent/network/dmz.yaml": `apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: dmz }
spec:
  vlan: 20
  ipv4:
    subnet: 10.0.20.0/24
    gateway: 10.0.20.1
    static: { range: 10.0.20.10-10.0.20.99 }
`,
	"intent/platform/template.yaml": `# Alpine template.
apiVersion: nodr/v1alpha1
kind: Template
metadata: { name: alpine }
spec:
  cluster: pve-main
  image: { url: https://example.com/alpine.qcow2, checksum: "sha256:0000000000000000000000000000000000000000000000000000000000000000" }
  storage: local-lvm
`,
	"intent/compute/web.yaml": `# Web servers.
apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-02
  labels:
    nodr/environment: prod
spec:
  placement:
    cluster: pve-main
    node: auto
  resources:
    cpu: { cores: 2 }
    memory: { size: 2Gi }
  nics:
    - network: dmz
      ipv4: { mode: auto }   # allocated by nodr
---
apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-01
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D3
  labels:
    nodr/environment: prod
spec:
  placement:
    cluster: pve-main
    node: pve2
  identity:
    vmid: 1000
  resources:
    cpu: { cores: 2 }
    memory: { size: 4Gi }
  nics:
    - network: dmz
      mac: BC:24:11:3A:5E:01
      ipv4:
        mode: auto
        address: 10.0.20.10/24
`,
}

var uidRE = regexp.MustCompile(`metadata\.uid = ([0-7][0-9A-HJKMNP-TV-Z]{25}) `)
var macRE = regexp.MustCompile(`spec\.nics\[0\]\.mac = ([0-9A-F]{2}(:[0-9A-F]{2}){5}) `)

func TestAdmit(t *testing.T) {
	root := writeWorkspace(t, admitFiles)
	file := filepath.Join(root, "intent", "compute", "web.yaml")
	src := admitFiles["intent/compute/web.yaml"]

	// web-01 comes first by name. web-02 goes to the node with less memory.
	want := `vm/web-01: spec.placement.assignedNode = pve2 (intent/compute/web.yaml)
vm/web-02: metadata.uid = UID (intent/compute/web.yaml)
vm/web-02: spec.placement.assignedNode = pve1 (intent/compute/web.yaml)
vm/web-02: spec.identity.vmid = 1001 (intent/compute/web.yaml)
vm/web-02: spec.nics[0].ipv4.address = 10.0.20.11/24 (intent/compute/web.yaml)
vm/web-02: spec.nics[0].mac = MAC (intent/compute/web.yaml)
template/alpine: spec.identity.vmid = 9000 (intent/platform/template.yaml)
`
	dry := run("admit", "-w", root, "--dry-run")
	dry.check(t, exitOK)
	dryOut := uidRE.ReplaceAllString(dry.stdout, "metadata.uid = UID ")
	dryOut = macRE.ReplaceAllString(dryOut, "spec.nics[0].mac = MAC ")
	if dryOut != want || dry.stderr != "" {
		t.Errorf("admit --dry-run: stdout =\n%s\nwant\n%s\nstderr = %q", dry.stdout, want, dry.stderr)
	}
	if data, _ := os.ReadFile(file); string(data) != src {
		t.Errorf("admit --dry-run changed the file:\n%s", data)
	}

	r := run("admit", "-w", root)
	r.check(t, exitOK)
	uidMatch := uidRE.FindStringSubmatch(r.stdout)
	macMatch := macRE.FindStringSubmatch(r.stdout)
	gotOut := r.stdout
	if uidMatch != nil {
		gotOut = strings.Replace(gotOut, uidMatch[1], "UID", 1)
	}
	if macMatch != nil {
		gotOut = strings.Replace(gotOut, macMatch[1], "MAC", 1)
	}
	if uidMatch == nil || macMatch == nil || gotOut != want {
		t.Fatalf("admit: stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	admitted := strings.NewReplacer(
		"  name: web-02\n", "  name: web-02\n  uid: "+uidMatch[1]+"\n",
		"    node: auto\n", "    node: auto\n    assignedNode: pve1\n  identity:\n    vmid: 1001\n",
		"    - network: dmz\n      ipv4: { mode: auto }", "    - network: dmz\n      mac: "+macMatch[1]+"\n      ipv4: { mode: auto, address: 10.0.20.11/24 }",
		"    node: pve2\n", "    node: pve2\n    assignedNode: pve2\n",
	).Replace(src)
	if data, _ := os.ReadFile(file); string(data) != admitted {
		t.Errorf("admit wrote\n%s\nwant\n%s", data, admitted)
	}

	templateFile := filepath.Join(root, "intent", "platform", "template.yaml")
	templateWant := strings.Replace(admitFiles["intent/platform/template.yaml"], "  storage: local-lvm\n", "  storage: local-lvm\n  identity:\n    vmid: 9000\n", 1)
	if data, _ := os.ReadFile(templateFile); string(data) != templateWant {
		t.Errorf("admit wrote template\n%s\nwant\n%s", data, templateWant)
	}
	// Admitting again changes nothing, and the workspace stays valid.
	again := run("admit", "-w", root)
	again.check(t, exitOK)
	if again.stdout != "" {
		t.Errorf("second admit: stdout = %q", again.stdout)
	}
	if data, _ := os.ReadFile(file); string(data) != admitted {
		t.Errorf("second admit changed the file:\n%s", data)
	}
	run("validate", "-w", root).check(t, exitOK)
}

func TestAdmitReservesGuestIDsAcrossVMsAndTemplates(t *testing.T) {
	files := map[string]string{}
	for name, content := range admitFiles {
		files[name] = content
	}
	files["nodr.yaml"] = strings.Replace(files["nodr.yaml"], "templates: { vmidRange: [9000, 9099] }", "templates: { vmidRange: [1000, 1999] }", 1)
	root := writeWorkspace(t, files)

	r := run("admit", "-w", root, "--dry-run")
	r.check(t, exitOK)
	if !strings.Contains(r.stdout, "vm/web-02: spec.identity.vmid = 1001 ") ||
		!strings.Contains(r.stdout, "template/alpine: spec.identity.vmid = 1002 ") {
		t.Errorf("admit guest IDs =\n%s\nwant VM 1001 then template 1002", r.stdout)
	}
}

func TestAdmitExplicitVMLeavesTemplateUnchanged(t *testing.T) {
	root := writeWorkspace(t, admitFiles)
	templateFile := filepath.Join(root, "intent", "platform", "template.yaml")
	templateSource := admitFiles["intent/platform/template.yaml"]

	r := run("admit", "-w", root, "vm/web-02")
	r.check(t, exitOK)
	if strings.Contains(r.stdout, "template/alpine:") {
		t.Errorf("explicit VM admission included a template assignment:\n%s", r.stdout)
	}
	if data, err := os.ReadFile(templateFile); err != nil {
		t.Fatal(err)
	} else if string(data) != templateSource {
		t.Errorf("explicit VM admission changed template:\n%s\nwant\n%s", data, templateSource)
	}
}

// TestAdmitMakesVMsRenderable follows a VM from intent without allocated
// values to rendered code.
func TestAdmitMakesVMsRenderable(t *testing.T) {
	root := writeWorkspace(t, admitFiles)
	r := run("render", "-w", root, "vm/web-02")
	r.check(t, exitError)
	if !strings.Contains(r.stderr, "not admitted yet; run 'nodr admit vm/web-02' first") {
		t.Errorf("render before admission: stderr = %q", r.stderr)
	}

	run("admit", "-w", root, "vm/web-02").check(t, exitOK)
	r = run("render", "-w", root, "vm/web-02")
	r.check(t, exitOK)
	code := strings.Join(strings.Fields(r.stdout), " ")
	for _, want := range []string{`node_name = "pve1"`, "vm_id = 1001", `address = "10.0.20.11/24" gateway = "10.0.20.1"`} {
		if !strings.Contains(code, want) {
			t.Errorf("render lacks %q:\n%s", want, r.stdout)
		}
	}
	// Only web-02 was admitted: web-01 still has no node.
	run("render", "-w", root, "vm/web-01").check(t, exitError)
}

func TestAdmitReportsProblems(t *testing.T) {
	files := map[string]string{}
	for name, content := range admitFiles {
		files[name] = content
	}
	files["intent/compute/web.yaml"] = strings.Replace(admitFiles["intent/compute/web.yaml"], "nodr/environment: prod\nspec:\n  placement:\n    cluster: pve-main\n    node: auto", "app: web\nspec:\n  placement:\n    cluster: pve-main\n    node: auto", 1)
	root := writeWorkspace(t, files)
	r := run("admit", "-w", root)
	r.check(t, exitError)
	want := `intent/compute/web.yaml:8: error: spec.identity.vmid: cannot allocate a guest ID: the label nodr/environment is missing, so the range of IDs is unknown
nodr: admission failed; no file was changed
`
	if r.stderr != want || r.stdout != "" {
		t.Errorf("stderr =\n%s\nwant\n%s\nstdout = %q", r.stderr, want, r.stdout)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "intent", "compute", "web.yaml")); string(data) != files["intent/compute/web.yaml"] {
		t.Errorf("a failed admission changed the file:\n%s", data)
	}
}

// TestAdmitDryRunFailsLikeARealRun checks that a dry run also fails when
// a value cannot be written.
func TestAdmitDryRunFailsLikeARealRun(t *testing.T) {
	files := map[string]string{}
	for name, content := range admitFiles {
		files[name] = content
	}
	// The interfaces share one mapping, so the address of the first cannot
	// be written without changing the second.
	files["intent/compute/web.yaml"] = strings.Replace(admitFiles["intent/compute/web.yaml"],
		"      ipv4: { mode: auto }   # allocated by nodr\n",
		"      ipv4: &auto { mode: auto }\n    - network: dmz\n      ipv4: *auto\n", 1)
	root := writeWorkspace(t, files)
	run("validate", "-w", root).check(t, exitOK)
	for _, args := range [][]string{{"admit", "-w", root, "--dry-run"}, {"admit", "-w", root}} {
		r := run(args...)
		r.check(t, exitError)
		if r.stdout != "" || !strings.Contains(r.stderr, "spec.nics.0.ipv4.address: inserting the field would change other content") {
			t.Errorf("%v: stdout = %q, stderr = %q", args, r.stdout, r.stderr)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(root, "intent", "compute", "web.yaml")); string(data) != files["intent/compute/web.yaml"] {
		t.Errorf("a failed admission changed the file:\n%s", data)
	}
}

func TestAdmitExample(t *testing.T) {
	r := run("admit", "-w", example, "--dry-run")
	r.check(t, exitOK)
	if r.stdout != "" || r.stderr != "" {
		t.Errorf("the example is admitted, but admit would write:\n%s%s", r.stdout, r.stderr)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"describe", "-w", example}, exitUsage, "expected one resource such as vm/web-01, got 0 arguments"},
		{[]string{"describe", "-w", example, "vm/web-01", "-o", "yaml"}, exitUsage, `unknown output format "yaml"`},
		{[]string{"describe", "-w", example, "web-01"}, exitUsage, "want <kind>/<name>"},
		{[]string{"render", "-w", example, "network/lan"}, exitUsage, "network/lan: only virtual machines are supported so far"},
		{[]string{"render", "-w", example, "vm/ghost"}, exitError, "vm/ghost does not exist in the workspace"},
		{[]string{"admit", "-w", example, "network/lan"}, exitUsage, "network/lan: only virtual machines are supported so far"},
		{[]string{"admit", "-w", example, "vm/ghost", "--dry-run"}, exitError, "vm/ghost does not exist in the workspace"},
		{[]string{"plan", "-w", example, "vm/web-01"}, exitUsage, `unexpected argument "vm/web-01"`},
		// The form of apply that submits documents to a server does not
		// exist yet.
		{[]string{"apply", "-w", example, "-f", "intent"}, exitUsage, "unknown shorthand flag: 'f' in -f"},
		{[]string{"validate", "--frobnicate"}, exitUsage, "unknown flag: --frobnicate"},
		{[]string{"version", "extra"}, exitUsage, `unexpected argument "extra"`},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			r := run(tt.args...)
			r.check(t, tt.code)
			if !strings.Contains(r.stderr, tt.want) {
				t.Errorf("stderr = %q, want %q", r.stderr, tt.want)
			}
			if tt.code == exitUsage && !strings.Contains(r.stderr, "Run 'nodr --help' for usage.") {
				t.Errorf("a usage error without the hint: %q", r.stderr)
			}
		})
	}
}
