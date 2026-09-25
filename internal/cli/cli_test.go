package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr)
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
