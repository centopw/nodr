package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/authn"
	"github.com/centopw/nodr/internal/secrets"
	"github.com/centopw/nodr/internal/workspace"
)

const testManifest = `apiVersion: nodr/v1alpha1
kind: Workspace
metadata: { name: homelab }
spec:
  environments:
    prod: { vmidRange: [1012, 1013] }
    lab: { vmidRange: [2000, 2999] }
    templates: { vmidRange: [9000, 9099] }
`

const testPlatform = `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://10.0.20.11:8006]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve1, pve2]
---
apiVersion: nodr/v1alpha1
kind: Template
metadata: { name: debian-12-cloud }
spec:
  cluster: pve-main
  image: { url: https://example.com/debian.qcow2, checksum: "sha256:0000000000000000000000000000000000000000000000000000000000000000" }
  storage: local-lvm
  identity: { vmid: 9000 }
`

const testNetwork = `apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: dmz }
spec:
  ipv4:
    subnet: 10.0.20.0/24
    gateway: 10.0.20.1
    static: { range: 10.0.20.23-10.0.20.23 }
`

const testVM = `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-01
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D3
  labels: { nodr/environment: prod }
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 1012 }
  resources: { cpu: { cores: 2 }, memory: { size: 8Gi } }
  nics:
    - network: dmz
      mac: BC:24:11:3A:5E:01
      ipv4: { mode: static, address: 10.0.20.21/24 }
`

func testWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"nodr.yaml":                  testManifest,
		"intent/platform/pve.yaml":   testPlatform,
		"intent/network/dmz.yaml":    testNetwork,
		"intent/compute/web-01.yaml": testVM,
	}
	for name, content := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func authedRequest(t *testing.T, h http.Handler, req *http.Request) *http.Request {
	t.Helper()
	loginReq := httptest.NewRequest(http.MethodPost, apiPrefix+"/auth/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login failed: status = %d, body = %s", loginRec.Code, loginRec.Body.String())
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	req.AddCookie(cookies[0])
	return req
}

func testHandlerWithAuth(t *testing.T, root string) http.Handler {
	t.Helper()
	dir := t.TempDir()
	authStore, err := authn.Open(filepath.Join(dir, "authn.db"))
	if err != nil {
		t.Fatalf("authn.Open: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })
	if err := authStore.CreateAccount(t.Context(), "admin", "test-password-123"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	kek := make([]byte, 32)
	secretsStore, err := secrets.Open(filepath.Join(dir, "secrets.db"), kek)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	t.Cleanup(func() { secretsStore.Close() })
	_ = secretsStore.Put(t.Context(), "proxmox/pve-main-token", []byte("default-test-token"))
	return Handler(t.Context(), root, authStore, secretsStore)
}

func testHandler(t *testing.T, root string) http.Handler {
	t.Helper()
	return testHandlerWithAuth(t, root)
}

func TestAuthGate_RejectsUnauthenticatedListWorkspaces(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuthGate_RejectsUnauthenticatedCreateVM(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/homelab/commands", strings.NewReader(`{"command":"vm.create","params":{}}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuthGate_AllowsAuthenticatedRequest(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)
	req := authedRequest(t, handler, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
}

func TestWorkspacePlan_ResolvesAndDoesNotLeakToken(t *testing.T) {
	root := testWorkspace(t)
	dir := t.TempDir()
	authStore, _ := authn.Open(filepath.Join(dir, "authn.db"))
	defer authStore.Close()
	authStore.CreateAccount(t.Context(), "admin", "test-password-123")
	kek := make([]byte, 32)
	secretsStore, _ := secrets.Open(filepath.Join(dir, "secrets.db"), kek)
	defer secretsStore.Close()
	secretsStore.Put(t.Context(), "proxmox/pve-main-token", []byte("plan-leak-check-token"))
	handler := Handler(t.Context(), root, authStore, secretsStore)

	fake := installFakeTofu(t)
	req := authedRequest(t, handler, httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/homelab/commands", strings.NewReader(`{"command":"workspace.plan"}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "plan-leak-check-token") {
		t.Errorf("response body leaked the token: %s", rec.Body.String())
	}
	log, _ := os.ReadFile(fake.log)
	if !strings.Contains(string(log), "plan-leak-check-token") {
		t.Errorf("fake tofu log = %q, want it to contain the resolved token (proves it reached the child process)", log)
	}
}

func TestWorkspacePlan_ErrorResponseDoesNotLeakToken(t *testing.T) {
	root := testWorkspace(t)
	dir := t.TempDir()
	authStore, _ := authn.Open(filepath.Join(dir, "authn.db"))
	defer authStore.Close()
	authStore.CreateAccount(t.Context(), "admin", "test-password-123")
	kek := make([]byte, 32)
	secretsStore, _ := secrets.Open(filepath.Join(dir, "secrets.db"), kek)
	defer secretsStore.Close()
	secretsStore.Put(t.Context(), "proxmox/pve-main-token", []byte("error-leak-check-token"))
	handler := Handler(t.Context(), root, authStore, secretsStore)

	installFakeTofu(t)
	t.Setenv("FAKE_TOFU_FAIL", "pve-main-compute init")
	req := authedRequest(t, handler, httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/homelab/commands", strings.NewReader(`{"command":"workspace.plan"}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "error-leak-check-token") {
		t.Errorf("error response leaked the token: %s", rec.Body.String())
	}
}

func request(t *testing.T, handler http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	authed := authedRequest(t, handler, req)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authed)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), out); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}

func TestListWorkspaces(t *testing.T) {
	response := request(t, testHandler(t, testWorkspace(t)), http.MethodGet, "/api/v1/workspaces", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got map[string]any
	decodeResponse(t, response, &got)
	want := map[string]any{"items": []any{map[string]any{"name": "homelab"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response = %#v, want %#v", got, want)
	}
}

func TestGetWorkspace(t *testing.T) {
	handler := testHandler(t, testWorkspace(t))
	response := request(t, handler, http.MethodGet, "/api/v1/workspaces/homelab", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got struct {
		Name         string `json:"name"`
		Environments map[string]struct {
			VMIDRange []int `json:"vmidRange"`
		} `json:"environments"`
	}
	decodeResponse(t, response, &got)
	if got.Name != "homelab" {
		t.Errorf("name = %q", got.Name)
	}
	want := map[string][]int{"prod": {1012, 1013}, "lab": {2000, 2999}}
	if len(got.Environments) != len(want) {
		t.Fatalf("environments = %#v, want %#v", got.Environments, want)
	}
	for name, value := range want {
		if !reflect.DeepEqual(got.Environments[name].VMIDRange, value) {
			t.Errorf("environment %s = %v, want %v", name, got.Environments[name].VMIDRange, value)
		}
	}
	if _, ok := got.Environments["templates"]; ok {
		t.Error("templates environment is user-facing")
	}

	response = request(t, handler, http.MethodGet, "/api/v1/workspaces/other", nil)
	checkProblem(t, response, http.StatusNotFound, "Workspace not found", "", "")
}

func TestListResources(t *testing.T) {
	handler := testHandler(t, testWorkspace(t))
	tests := map[string]any{
		"VirtualMachine": map[string]any{
			"items": []any{map[string]any{
				"kind": "VirtualMachine", "name": "web-01", "cluster": "pve-main", "node": "pve1",
				"vmid": float64(1012), "cpu": float64(2), "memory": "8Gi", "environment": "prod",
				"addresses":  []any{"10.0.20.21/24"},
				"powerState": "running",
			}},
		},
		"ProxmoxCluster": map[string]any{"items": []any{map[string]any{"kind": "ProxmoxCluster", "name": "pve-main"}}},
		"Network":        map[string]any{"items": []any{map[string]any{"kind": "Network", "name": "dmz"}}},
		"Template":       map[string]any{"items": []any{map[string]any{"kind": "Template", "name": "debian-12-cloud"}}},
	}
	for kind, want := range tests {
		t.Run(kind, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, "/api/v1/workspaces/homelab/resources?kind="+kind, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var got any
			decodeResponse(t, response, &got)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("response = %#v, want %#v", got, want)
			}
		})
	}

	for name, target := range map[string]string{
		"missing":     "/api/v1/workspaces/homelab/resources",
		"unsupported": "/api/v1/workspaces/homelab/resources?kind=SSHKey",
	} {
		t.Run(name, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, target, nil)
			checkProblem(t, response, http.StatusBadRequest, "Invalid resource kind", "kind", "")
		})
	}
}

func TestListResourcesIncludesUnadmittedVM(t *testing.T) {
	root := testWorkspace(t)
	pending := `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata: { name: web-02 }
spec:
  placement: { cluster: pve-main }
  resources: { cpu: { cores: 2 }, memory: { size: 4Gi } }
`
	if err := os.WriteFile(filepath.Join(root, "intent", "compute", "web-02.yaml"), []byte(pending), 0o644); err != nil {
		t.Fatal(err)
	}
	response := request(t, testHandler(t, root), http.MethodGet, "/api/v1/workspaces/homelab/resources?kind=VirtualMachine", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got struct {
		Items []virtualMachineItem `json:"items"`
	}
	decodeResponse(t, response, &got)
	var pending2 *virtualMachineItem
	for i := range got.Items {
		if got.Items[i].Name == "web-02" {
			pending2 = &got.Items[i]
		}
	}
	if pending2 == nil {
		t.Fatalf("response missing unadmitted VM: %#v", got.Items)
	}
	if pending2.VMID != 0 || pending2.Node != "" || len(pending2.Addresses) != 0 {
		t.Errorf("unadmitted VM = %#v, want vmid 0, node \"\", no addresses", pending2)
	}
}
func TestAPIPathAndMethodErrorsUseProblemDetails(t *testing.T) {
	handler := testHandler(t, testWorkspace(t))
	for _, test := range []struct {
		name   string
		method string
		target string
		status int
		title  string
	}{
		{name: "unknown path", method: http.MethodGet, target: "/api/v1/unknown", status: http.StatusNotFound, title: "Not found"},
		{name: "wrong method", method: http.MethodDelete, target: "/api/v1/workspaces", status: http.StatusMethodNotAllowed, title: "Method not allowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, handler, test.method, test.target, nil)
			checkProblem(t, response, test.status, test.title, "", "")
		})
	}
}

func validCreateCommand() map[string]any {
	return map[string]any{
		"command": "vm.create",
		"params": map[string]any{
			"name": "web-03", "environment": "prod", "cluster": "pve-main",
			"template": "debian-12-cloud", "network": "dmz", "storage": "local-lvm", "size": "M",
		},
	}
}

func TestCreateVM(t *testing.T) {
	root := testWorkspace(t)
	response := request(t, testHandler(t, root), http.MethodPost, "/api/v1/workspaces/homelab/commands", validCreateCommand())
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got struct {
		Name      string   `json:"name"`
		Cluster   string   `json:"cluster"`
		Node      string   `json:"node"`
		VMID      int      `json:"vmid"`
		CPU       int      `json:"cpu"`
		Memory    string   `json:"memory"`
		Addresses []string `json:"addresses"`
		MACs      []string `json:"macs"`
		Summary   string   `json:"summary"`
	}
	decodeResponse(t, response, &got)
	if got.Name != "web-03" || got.Cluster != "pve-main" || got.Node != "pve2" || got.VMID != 1013 || got.CPU != 2 || got.Memory != "4Gi" {
		t.Errorf("response = %#v", got)
	}
	if !reflect.DeepEqual(got.Addresses, []string{"10.0.20.23/24"}) {
		t.Errorf("addresses = %v", got.Addresses)
	}
	if len(got.MACs) != 1 || !regexp.MustCompile(`^BC:24:11:([0-9A-F]{2}:){2}[0-9A-F]{2}$`).MatchString(got.MACs[0]) {
		t.Errorf("macs = %v", got.MACs)
	}
	wantSummary := "Creates web-03 with 2 vCPUs and 4 GiB memory on pve2, IP 10.0.20.23."
	if got.Summary != wantSummary {
		t.Errorf("summary = %q, want %q", got.Summary, wantSummary)
	}

	ws, diags := workspace.Load(root)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if diags := ws.Validate(reg); diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	doc := ws.Find(nrm.Ref{Kind: v1alpha1.KindVirtualMachine, Name: "web-03"})
	if doc == nil {
		t.Fatal("created VM is absent from workspace")
	}
	if doc.File != "intent/compute/web-03.yaml" {
		t.Errorf("file = %q", doc.File)
	}
	vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](doc)
	if err != nil {
		t.Fatal(err)
	}
	if vm.Metadata.Labels[nrm.LabelEnvironment] != "prod" || vm.Spec.Source.Template != "debian-12-cloud" {
		t.Errorf("created VM metadata/source = %#v / %#v", vm.Metadata, vm.Spec.Source)
	}
	if vm.Spec.Resources.CPU.Cores != 2 || vm.Spec.Resources.Memory.Size.String() != "4Gi" {
		t.Errorf("created VM resources = %#v", vm.Spec.Resources)
	}
	if len(vm.Spec.Disks) != 1 || vm.Spec.Disks[0].Name != "root" || vm.Spec.Disks[0].Storage != "local-lvm" || vm.Spec.Disks[0].Size.String() != "40Gi" {
		t.Errorf("created VM disks = %#v", vm.Spec.Disks)
	}
	if len(vm.Spec.NICs) != 1 || vm.Spec.NICs[0].Network != "dmz" || vm.Spec.NICs[0].IPv4 == nil || vm.Spec.NICs[0].IPv4.Mode != "auto" {
		t.Errorf("created VM nics = %#v", vm.Spec.NICs)
	}
	if vm.Spec.Identity.VMID != got.VMID || vm.Spec.Placement.AssignedNode != got.Node || vm.Spec.NICs[0].MAC != got.MACs[0] || vm.Spec.NICs[0].IPv4.Address != got.Addresses[0] {
		t.Errorf("allocated file values do not match response: vm = %#v, response = %#v", vm.Spec, got)
	}
}

func TestCreateVMValidation(t *testing.T) {
	tests := []struct {
		name       string
		field      string
		value      any
		status     int
		path       string
		messageHas string
	}{
		{name: "name required", field: "name", value: "", status: 400, path: "params.name", messageHas: "required"},
		{name: "bad name", field: "name", value: "Web_03", status: 400, path: "params.name", messageHas: "lowercase"},
		{name: "environment required", field: "environment", value: "", status: 400, path: "params.environment", messageHas: "required"},
		{name: "unknown environment", field: "environment", value: "staging", status: 400, path: "params.environment", messageHas: "does not exist"},
		{name: "templates environment", field: "environment", value: "templates", status: 400, path: "params.environment", messageHas: "not available"},
		{name: "cluster required", field: "cluster", value: "", status: 400, path: "params.cluster", messageHas: "required"},
		{name: "unknown cluster", field: "cluster", value: "other", status: 400, path: "params.cluster", messageHas: "does not exist"},
		{name: "template required", field: "template", value: "", status: 400, path: "params.template", messageHas: "required"},
		{name: "unknown template", field: "template", value: "ubuntu", status: 400, path: "params.template", messageHas: "does not exist"},
		{name: "network required", field: "network", value: "", status: 400, path: "params.network", messageHas: "required"},
		{name: "unknown network", field: "network", value: "lan", status: 400, path: "params.network", messageHas: "does not exist"},
		{name: "storage required", field: "storage", value: "", status: 400, path: "params.storage", messageHas: "required"},
		{name: "size required", field: "size", value: "", status: 400, path: "params.size", messageHas: "required"},
		{name: "bad size", field: "size", value: "XL", status: 400, path: "params.size", messageHas: "S, M or L"},
		{name: "duplicate", field: "name", value: "web-01", status: 409, path: "params.name", messageHas: "already exists"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := testWorkspace(t)
			command := validCreateCommand()
			command["params"].(map[string]any)[test.field] = test.value
			response := request(t, testHandler(t, root), http.MethodPost, "/api/v1/workspaces/homelab/commands", command)
			checkProblem(t, response, test.status, "", test.path, test.messageHas)
			if _, err := os.Stat(filepath.Join(root, "intent", "compute", "web-03.yaml")); !os.IsNotExist(err) {
				t.Errorf("invalid request left an intent file: %v", err)
			}
		})
	}
}

func TestCreateVMRejectsUnknownCommand(t *testing.T) {
	root := testWorkspace(t)
	response := request(t, testHandler(t, root), http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{"command": "vm.destroy"})
	checkProblem(t, response, http.StatusBadRequest, "Unknown command", "command", "vm.destroy")
}

func TestSizeCatalogAndSummaryWithoutAddress(t *testing.T) {
	wantSizes := map[string]size{
		"S": {CPU: 1, Memory: "2Gi", Disk: "20Gi"},
		"M": {CPU: 2, Memory: "4Gi", Disk: "40Gi"},
		"L": {CPU: 4, Memory: "8Gi", Disk: "80Gi"},
	}
	if !reflect.DeepEqual(sizes, wantSizes) {
		t.Errorf("sizes = %#v, want %#v", sizes, wantSizes)
	}

	documents, diags := nrm.Parse("intent/compute/no-ip.yaml", []byte(`apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata: { name: no-ip }
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 1013 }
  resources: { cpu: { cores: 1 }, memory: { size: 2Gi } }
  nics: [{ network: dmz, mac: BC:24:11:00:00:01, ipv4: { mode: dhcp } }]
`))
	if diags.HasErrors() || len(documents) != 1 {
		t.Fatalf("parse VM: %v", diags.Err())
	}
	vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](documents[0])
	if err != nil {
		t.Fatal(err)
	}
	response := createResponse(vm)
	if response.Summary != "Creates no-ip with 1 vCPUs and 2 GiB memory on pve1." {
		t.Errorf("summary = %q", response.Summary)
	}
	if response.Addresses == nil || response.MACs == nil {
		t.Errorf("network arrays must not be nil: addresses=%v macs=%v", response.Addresses, response.MACs)
	}
}

func TestCreateVMSerializesAdmission(t *testing.T) {
	root := testWorkspace(t)
	manifest := strings.Replace(testManifest, "[1012, 1013]", "[1012, 1014]", 1)
	if err := os.WriteFile(filepath.Join(root, "nodr.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	network := strings.Replace(testNetwork, "10.0.20.23-10.0.20.23", "10.0.20.23-10.0.20.24", 1)
	if err := os.WriteFile(filepath.Join(root, "intent", "network", "dmz.yaml"), []byte(network), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := testHandler(t, root)
	loginReq := httptest.NewRequest(http.MethodPost, apiPrefix+"/auth/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login failed: %s", loginRec.Body.String())
	}
	cookie := loginRec.Result().Cookies()[0]

	responses := make([]*httptest.ResponseRecorder, 2)
	var wait sync.WaitGroup
	for i, name := range []string{"web-03", "web-04"} {
		wait.Add(1)
		go func(idx int, vmName string) {
			defer wait.Done()
			command := validCreateCommand()
			command["params"].(map[string]any)["name"] = vmName
			data, err := json.Marshal(command)
			if err != nil {
				return
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/homelab/commands", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			responses[idx] = rec
		}(i, name)
	}
	wait.Wait()
	for i, response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("response %d status = %d, body = %s", i, response.Code, response.Body.String())
		}
	}

	ws, diags := workspace.Load(root)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if diags := ws.Validate(reg); diags.HasErrors() {
		t.Fatalf("concurrent creations left invalid intent: %v", diags.Err())
	}
}

func TestCreateVMRollsBackAdmissionError(t *testing.T) {
	root := testWorkspace(t)
	exhausted := strings.Replace(testManifest, "[1012, 1013]", "[1012, 1012]", 1)
	if err := os.WriteFile(filepath.Join(root, "nodr.yaml"), []byte(exhausted), 0o644); err != nil {
		t.Fatal(err)
	}
	response := request(t, testHandler(t, root), http.MethodPost, "/api/v1/workspaces/homelab/commands", validCreateCommand())
	checkProblem(t, response, http.StatusBadRequest, "Admission failed", "spec.identity.vmid", "uses every ID")
	if _, err := os.Stat(filepath.Join(root, "intent", "compute", "web-03.yaml")); !os.IsNotExist(err) {
		t.Errorf("admission failure left an intent file: %v", err)
	}
}

func TestDiagnosticProblemOmitsUnlocatedErrors(t *testing.T) {
	var diags diag.List
	diags.Errorf("", 0, "", "workspace cannot be read")
	response := httptest.NewRecorder()
	writeDiagnosticProblem(response, http.StatusInternalServerError, "Workspace load failed", diags)
	var body map[string]any
	decodeResponse(t, response, &body)
	if _, exists := body["errors"]; exists {
		t.Errorf("problem with one unlocated diagnostic includes errors: %#v", body)
	}
}

func checkProblem(t *testing.T, response *httptest.ResponseRecorder, status int, title, path, messageHas string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d, body = %s", response.Code, status, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Errorf("Content-Type = %q", contentType)
	}
	var problem struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
		Errors []struct {
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	decodeResponse(t, response, &problem)
	if problem.Type != "about:blank" || problem.Status != status {
		t.Errorf("problem = %#v", problem)
	}
	if title != "" && problem.Title != title {
		t.Errorf("title = %q, want %q", problem.Title, title)
	}
	if path != "" {
		found := false
		for _, item := range problem.Errors {
			if item.Path == path && strings.Contains(item.Message, messageHas) {
				found = true
			}
		}
		if !found {
			t.Errorf("errors = %#v, want path %q containing %q", problem.Errors, path, messageHas)
		}
	} else if messageHas != "" && !strings.Contains(problem.Detail, messageHas) {
		t.Errorf("detail = %q, want it to contain %q", problem.Detail, messageHas)
	}
}

const fakeTofuScript = `#!/bin/sh
unit=$(pwd -P)
unit=${unit##*/}
echo "$unit $* token=$PROXMOX_VE_API_TOKEN" >> "$FAKE_TOFU_LOG"
for arg in "$@"; do last=$arg; done
if [ "$FAKE_TOFU_FAIL" = "$unit $1" ]; then
	echo "Error: $1 failed in $unit with token $PROXMOX_VE_API_TOKEN" >&2
	exit 1
fi
case $1 in
init)
	echo "initialized $unit with the token $PROXMOX_VE_API_TOKEN"
	;;
plan)
	for arg in "$@"; do
		case $arg in -out=*) plan=${arg#-out=} ;; esac
	done
	echo "plan of $unit" > "$plan"
	echo "planned $unit"
	;;
show | apply)
	if [ "$(cat "$last")" != "plan of $unit" ]; then
		echo "Error: $last is not a saved plan of $unit" >&2
		exit 1
	fi
	if [ "$1" = apply ]; then
		echo "applied $unit"
	elif [ -f "$FAKE_TOFU_PLANS/$unit.json" ]; then
		cat "$FAKE_TOFU_PLANS/$unit.json"
	else
		echo '{"format_version": "1.2", "resource_changes": []}'
	fi
	;;
*)
	echo "Error: unexpected command $1" >&2
	exit 1
	;;
esac
`

type fakeTofu struct {
	log, plans string
}

func installFakeTofu(t *testing.T) *fakeTofu {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tofu is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "tofu")
	if err := os.WriteFile(bin, []byte(fakeTofuScript), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeTofu{log: filepath.Join(dir, "calls.log"), plans: filepath.Join(dir, "plans")}
	if err := os.Mkdir(f.plans, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODR_TOFU", bin)
	t.Setenv("FAKE_TOFU_LOG", f.log)
	t.Setenv("FAKE_TOFU_PLANS", f.plans)
	t.Setenv("FAKE_TOFU_FAIL", "")
	return f
}

//nolint:unparam // unit is parameterized for test versatility across different state units
func (f *fakeTofu) setPlan(t *testing.T, unit string, changes ...string) {
	t.Helper()
	type resourceChange struct {
		Address string `json:"address"`
		Type    string `json:"type"`
		Change  struct {
			Actions []string `json:"actions"`
		} `json:"change"`
	}
	plan := struct {
		FormatVersion   string           `json:"format_version"`
		ResourceChanges []resourceChange `json:"resource_changes"`
	}{FormatVersion: "1.2", ResourceChanges: []resourceChange{}}
	for _, c := range changes {
		fields := strings.Fields(c)
		rc := resourceChange{Address: fields[1]}
		rc.Type, _, _ = strings.Cut(rc.Address, ".")
		rc.Change.Actions = strings.Split(fields[0], ",")
		plan.ResourceChanges = append(plan.ResourceChanges, rc)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.plans, unit+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePlan(t *testing.T) {
	tofu := installFakeTofu(t)
	root := testWorkspace(t)
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")

	handler := testHandler(t, root)
	cmd := map[string]any{
		"command": "workspace.plan",
		"params":  map[string]any{},
	}
	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", cmd)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var planResp workspacePlanResponse
	decodeResponse(t, res, &planResp)
	if planResp.PlanID == "" {
		t.Errorf("planId is empty")
	}
	if !planResp.HasChanges {
		t.Errorf("expected HasChanges = true")
	}
	if planResp.HasDestructiveChanges {
		t.Errorf("expected HasDestructiveChanges = false")
	}
	if len(planResp.Units) != 1 {
		t.Fatalf("expected 1 unit, got %d", len(planResp.Units))
	}
	u := planResp.Units[0]
	if u.Dir != "terraform/pve-main-compute" {
		t.Errorf("u.Dir = %q, want terraform/pve-main-compute", u.Dir)
	}
	if !u.HasChanges || u.Destructive {
		t.Errorf("u.HasChanges = %v, u.Destructive = %v", u.HasChanges, u.Destructive)
	}
	if u.Summary.Create != 1 {
		t.Errorf("u.Summary.Create = %d, want 1", u.Summary.Create)
	}
	if len(u.Changes) != 1 || u.Changes[0].Action != "create" || u.Changes[0].Address != "proxmox_virtual_environment_vm.web_01" {
		t.Errorf("u.Changes = %#v", u.Changes)
	}
}

func TestWorkspacePlanNoChanges(t *testing.T) {
	installFakeTofu(t)
	root := testWorkspace(t)
	handler := testHandler(t, root)
	cmd := map[string]any{
		"command": "workspace.plan",
		"params":  map[string]any{},
	}
	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", cmd)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var planResp workspacePlanResponse
	decodeResponse(t, res, &planResp)
	if planResp.HasChanges {
		t.Errorf("expected HasChanges = false")
	}
	if len(planResp.Units) != 1 {
		t.Fatalf("expected 1 unit, got %d", len(planResp.Units))
	}
	if planResp.Units[0].HasChanges {
		t.Errorf("expected unit HasChanges = false")
	}
	if len(planResp.Units[0].Changes) != 0 {
		t.Errorf("expected empty changes, got %#v", planResp.Units[0].Changes)
	}
}

func TestWorkspacePlanCompileError(t *testing.T) {
	installFakeTofu(t)
	root := testWorkspace(t)
	vmFile := filepath.Join(root, "intent", "compute", "web-01.yaml")
	data, _ := os.ReadFile(vmFile)
	badData := strings.Replace(string(data), ", assignedNode: pve1", "", 1)
	if err := os.WriteFile(vmFile, []byte(badData), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := testHandler(t, root)
	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.plan",
		"params":  map[string]any{},
	})
	checkProblem(t, res, http.StatusBadRequest, "Compilation failed", "", "")
}

func TestWorkspacePlanMissingTofu(t *testing.T) {
	root := testWorkspace(t)
	t.Setenv("NODR_TOFU", "")
	t.Setenv("PATH", t.TempDir())
	handler := testHandler(t, root)
	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.plan",
		"params":  map[string]any{},
	})
	checkProblem(t, res, http.StatusServiceUnavailable, "OpenTofu not found", "", "OpenTofu is not installed or not on PATH; install it or set NODR_TOFU")
}

func TestWorkspacePlanUnitCommandFails(t *testing.T) {
	installFakeTofu(t)
	root := testWorkspace(t)
	t.Setenv("FAKE_TOFU_FAIL", "pve-main-compute init")
	handler := testHandler(t, root)
	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.plan",
		"params":  map[string]any{},
	})
	checkProblem(t, res, http.StatusBadGateway, "OpenTofu error", "", "init failed in pve-main-compute")
}

func TestWorkspacePlanConflict(t *testing.T) {
	installFakeTofu(t)
	root := testWorkspace(t)
	s := &server{
		ctx:   t.Context(),
		root:  root,
		plans: make(map[string]*storedPlan),
	}
	s.planMu.Lock()
	defer s.planMu.Unlock()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/homelab/commands", strings.NewReader(`{"command":"workspace.plan","params":{}}`))
	r.SetPathValue("workspace", "homelab")
	s.runCommand(w, r)
	checkProblem(t, w, http.StatusConflict, "Conflict", "", "another plan or apply is already running")
}

func TestWorkspaceApplySuccess(t *testing.T) {
	tofu := installFakeTofu(t)
	root := testWorkspace(t)
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")
	handler := testHandler(t, root)

	// Step 1: plan
	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.plan",
		"params":  map[string]any{},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("plan status = %d, body = %s", res.Code, res.Body.String())
	}
	var planResp workspacePlanResponse
	decodeResponse(t, res, &planResp)

	// Step 2: apply
	applyRes := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.apply",
		"params":  map[string]any{"planId": planResp.PlanID},
	})
	if applyRes.Code != http.StatusOK {
		t.Fatalf("apply status = %d, body = %s", applyRes.Code, applyRes.Body.String())
	}
	var applyResp workspaceApplyResponse
	decodeResponse(t, applyRes, &applyResp)
	if len(applyResp.Units) != 1 || applyResp.Units[0].Dir != "terraform/pve-main-compute" || applyResp.Units[0].Outcome != "applied" {
		t.Errorf("applyResp.Units = %#v", applyResp.Units)
	}

	// Step 3: second apply should fail with 404
	reApplyRes := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.apply",
		"params":  map[string]any{"planId": planResp.PlanID},
	})
	checkProblem(t, reApplyRes, http.StatusNotFound, "Plan not found", "", "")
}

func TestWorkspaceApplyValidation(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)

	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.apply",
		"params":  map[string]any{},
	})
	checkProblem(t, res, http.StatusBadRequest, "Invalid request", "params.planId", "required")

	res = request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.apply",
		"params":  map[string]any{"planId": "nonexistent"},
	})
	checkProblem(t, res, http.StatusNotFound, "Plan not found", "", "nonexistent")
}

func TestWorkspaceApplyExpiry(t *testing.T) {
	tofu := installFakeTofu(t)
	root := testWorkspace(t)
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")

	dir := t.TempDir()
	authStore, err := authn.Open(filepath.Join(dir, "authn.db"))
	if err != nil {
		t.Fatalf("authn.Open: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })
	if err := authStore.CreateAccount(t.Context(), "admin", "test-password-123"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	kek := make([]byte, 32)
	secretsStore, err := secrets.Open(filepath.Join(dir, "secrets.db"), kek)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	t.Cleanup(func() { secretsStore.Close() })
	_ = secretsStore.Put(t.Context(), "proxmox/pve-main-token", []byte("default-test-token"))

	s := &server{
		ctx:     t.Context(),
		root:    root,
		plans:   make(map[string]*storedPlan),
		secrets: secretsStore,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+apiPrefix+"/workspaces", s.listWorkspaces)
	mux.HandleFunc("GET "+apiPrefix+"/workspaces/{workspace}", s.getWorkspace)
	mux.HandleFunc("GET "+apiPrefix+"/workspaces/{workspace}/resources", s.listResources)
	mux.HandleFunc("POST "+apiPrefix+"/workspaces/{workspace}/commands", s.runCommand)
	mux.HandleFunc("POST "+apiPrefix+"/workspaces/{workspace}/resources/{kind}/{name}", s.resourceAction)
	mux.HandleFunc("DELETE "+apiPrefix+"/workspaces/{workspace}/resources/{kind}/{name}", s.deleteResource)
	mux.HandleFunc(apiPrefix+"/workspaces", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}/resources", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}/commands", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}/resources/{kind}/{name}", methodNotAllowed)
	mux.HandleFunc("/api/", notFound)

	rootMux := http.NewServeMux()
	rootMux.HandleFunc("POST "+apiPrefix+"/auth/login", func(w http.ResponseWriter, r *http.Request) {
		authn.LoginHandler(authStore, apiPrefix+"/auth/login").ServeHTTP(w, r)
	})
	rootMux.HandleFunc("POST "+apiPrefix+"/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		authn.LogoutHandler(authStore, apiPrefix+"/auth/logout").ServeHTTP(w, r)
	})
	rootMux.Handle("/", authn.Middleware(authStore, apiPrefix+"/auth/login", apiPrefix+"/auth/logout")(mux))

	planRes := request(t, rootMux, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.plan",
		"params":  map[string]any{},
	})
	var planResp workspacePlanResponse
	decodeResponse(t, planRes, &planResp)

	s.plansMu.Lock()
	s.plans[planResp.PlanID].createdAt = time.Now().Add(-16 * time.Minute)
	s.plansMu.Unlock()

	applyRes := request(t, rootMux, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.apply",
		"params":  map[string]any{"planId": planResp.PlanID},
	})
	checkProblem(t, applyRes, http.StatusNotFound, "Plan not found", "", "expired")
}

func TestWorkspaceApplyDestructiveRejection(t *testing.T) {
	tofu := installFakeTofu(t)
	root := testWorkspace(t)
	tofu.setPlan(t, "pve-main-compute", "delete proxmox_virtual_environment_vm.web_01")
	handler := testHandler(t, root)

	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.plan",
		"params":  map[string]any{},
	})
	var planResp workspacePlanResponse
	decodeResponse(t, res, &planResp)
	if !planResp.HasDestructiveChanges {
		t.Fatalf("expected HasDestructiveChanges = true")
	}

	applyRes := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.apply",
		"params":  map[string]any{"planId": planResp.PlanID, "allowDestroy": false},
	})
	checkProblem(t, applyRes, http.StatusBadRequest, "Destructive changes require approval", "", "replaces or destroys resources")

	applyRes = request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.apply",
		"params":  map[string]any{"planId": planResp.PlanID, "allowDestroy": true},
	})
	if applyRes.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", applyRes.Code, applyRes.Body.String())
	}
}

func TestWorkspaceApplyCommandFails(t *testing.T) {
	tofu := installFakeTofu(t)
	root := testWorkspace(t)
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")
	handler := testHandler(t, root)

	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.plan",
		"params":  map[string]any{},
	})
	var planResp workspacePlanResponse
	decodeResponse(t, res, &planResp)

	t.Setenv("FAKE_TOFU_FAIL", "pve-main-compute apply")
	applyRes := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "workspace.apply",
		"params":  map[string]any{"planId": planResp.PlanID},
	})
	if applyRes.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body = %s", applyRes.Code, applyRes.Body.String())
	}
	var prob problem
	decodeResponse(t, applyRes, &prob)
	if len(prob.Units) != 1 || prob.Units[0].Outcome != "failed" {
		t.Errorf("prob.Units = %#v", prob.Units)
	}
	if !strings.Contains(prob.Detail, "apply failed in pve-main-compute") {
		t.Errorf("prob.Detail = %q, want to contain apply failed", prob.Detail)
	}
}

func TestWorkspaceApplyConflict(t *testing.T) {
	installFakeTofu(t)
	root := testWorkspace(t)
	s := &server{
		ctx:   t.Context(),
		root:  root,
		plans: make(map[string]*storedPlan),
	}
	s.planMu.Lock()
	defer s.planMu.Unlock()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/homelab/commands", strings.NewReader(`{"command":"workspace.apply","params":{"planId":"01HXYZ"}}`))
	r.SetPathValue("workspace", "homelab")
	s.runCommand(w, r)
	checkProblem(t, w, http.StatusConflict, "Conflict", "", "another plan or apply is already running")
}

func TestVMLifecycleCommands(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)

	// 1. Initial power state should default to "running" in listResources
	res := request(t, handler, http.MethodGet, "/api/v1/workspaces/homelab/resources?kind=VirtualMachine", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("listResources status = %d: %s", res.Code, res.Body.String())
	}
	var list struct {
		Items []virtualMachineItem `json:"items"`
	}
	decodeResponse(t, res, &list)
	if len(list.Items) != 1 || list.Items[0].PowerState != "running" {
		t.Fatalf("unexpected items: %#v", list.Items)
	}

	// 2. Stop the VM via vm.stop command
	stopRes := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "vm.stop",
		"params":  map[string]any{"name": "web-01"},
	})
	if stopRes.Code != http.StatusOK {
		t.Fatalf("vm.stop status = %d: %s", stopRes.Code, stopRes.Body.String())
	}
	var stopBody vmPowerStateResponse
	decodeResponse(t, stopRes, &stopBody)
	if stopBody.Name != "web-01" || stopBody.PowerState != "stopped" {
		t.Errorf("stopBody = %#v, want name web-01, powerState stopped", stopBody)
	}

	// Verify persistence in YAML file
	content, err := os.ReadFile(filepath.Join(root, "intent", "compute", "web-01.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "powerState: stopped") {
		t.Errorf("yaml content missing powerState: stopped: %s", string(content))
	}

	// Verify listResources reports stopped
	res = request(t, handler, http.MethodGet, "/api/v1/workspaces/homelab/resources?kind=VirtualMachine", nil)
	decodeResponse(t, res, &list)
	if len(list.Items) != 1 || list.Items[0].PowerState != "stopped" {
		t.Fatalf("expected powerState stopped, got %#v", list.Items)
	}

	// 3. Start the VM via vm.start command
	startRes := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "vm.start",
		"params":  map[string]any{"name": "web-01"},
	})
	if startRes.Code != http.StatusOK {
		t.Fatalf("vm.start status = %d: %s", startRes.Code, startRes.Body.String())
	}
	var startBody vmPowerStateResponse
	decodeResponse(t, startRes, &startBody)
	if startBody.Name != "web-01" || startBody.PowerState != "running" {
		t.Errorf("startBody = %#v, want name web-01, powerState running", startBody)
	}

	content, err = os.ReadFile(filepath.Join(root, "intent", "compute", "web-01.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "powerState: running") {
		t.Errorf("yaml content missing powerState: running: %s", string(content))
	}

	// 4. Delete the VM via vm.delete command
	delRes := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "vm.delete",
		"params":  map[string]any{"name": "web-01"},
	})
	if delRes.Code != http.StatusOK {
		t.Fatalf("vm.delete status = %d: %s", delRes.Code, delRes.Body.String())
	}
	var delBody vmDeleteResponse
	decodeResponse(t, delRes, &delBody)
	if delBody.Name != "web-01" || !delBody.Deleted {
		t.Errorf("delBody = %#v, want name web-01, deleted true", delBody)
	}

	// Verify file is removed
	if _, err := os.Stat(filepath.Join(root, "intent", "compute", "web-01.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected file removed, err = %v", err)
	}

	// Subsequent operations on web-01 return 404
	subsequentRes := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "vm.stop",
		"params":  map[string]any{"name": "web-01"},
	})
	checkProblem(t, subsequentRes, http.StatusNotFound, "Resource not found", "", "web-01")

	// listResources returns 0 items
	res = request(t, handler, http.MethodGet, "/api/v1/workspaces/homelab/resources?kind=VirtualMachine", nil)
	decodeResponse(t, res, &list)
	if len(list.Items) != 0 {
		t.Errorf("expected 0 items, got %#v", list.Items)
	}
}

func TestVMLifecycleRESTEndpoints(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)

	// Stop via :stop POST
	stopRes := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/resources/VirtualMachine/web-01:stop", nil)
	if stopRes.Code != http.StatusOK {
		t.Fatalf("REST :stop status = %d: %s", stopRes.Code, stopRes.Body.String())
	}
	var stopBody vmPowerStateResponse
	decodeResponse(t, stopRes, &stopBody)
	if stopBody.PowerState != "stopped" {
		t.Errorf("expected stopped, got %q", stopBody.PowerState)
	}

	// Start via :start POST
	startRes := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/resources/VirtualMachine/web-01:start", nil)
	if startRes.Code != http.StatusOK {
		t.Fatalf("REST :start status = %d: %s", startRes.Code, startRes.Body.String())
	}
	var startBody vmPowerStateResponse
	decodeResponse(t, startRes, &startBody)
	if startBody.PowerState != "running" {
		t.Errorf("expected running, got %q", startBody.PowerState)
	}

	// Delete via DELETE
	delRes := request(t, handler, http.MethodDelete, "/api/v1/workspaces/homelab/resources/VirtualMachine/web-01", nil)
	if delRes.Code != http.StatusOK {
		t.Fatalf("REST DELETE status = %d: %s", delRes.Code, delRes.Body.String())
	}
	var delBody vmDeleteResponse
	decodeResponse(t, delRes, &delBody)
	if !delBody.Deleted {
		t.Errorf("expected deleted true, got false")
	}

	// Verify file is removed
	if _, err := os.Stat(filepath.Join(root, "intent", "compute", "web-01.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected file removed, err = %v", err)
	}

	// Subsequent DELETE returns 404
	del404 := request(t, handler, http.MethodDelete, "/api/v1/workspaces/homelab/resources/VirtualMachine/web-01", nil)
	checkProblem(t, del404, http.StatusNotFound, "Resource not found", "", "web-01")
}

func TestVMLifecycleRESTDeleteSuffix(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)

	// Delete via POST ...:delete
	delRes := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/resources/VirtualMachine/web-01:delete", nil)
	if delRes.Code != http.StatusOK {
		t.Fatalf("REST :delete status = %d: %s", delRes.Code, delRes.Body.String())
	}
	var delBody vmDeleteResponse
	decodeResponse(t, delRes, &delBody)
	if !delBody.Deleted {
		t.Errorf("expected deleted true, got false")
	}
	if _, err := os.Stat(filepath.Join(root, "intent", "compute", "web-01.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected file removed, err = %v", err)
	}
}

func TestVMLifecycleValidationErrors(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)

	// 1. Missing name parameter
	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "vm.start",
	})
	checkProblem(t, res, http.StatusBadRequest, "Invalid request", "name", "name is required")

	res = request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "vm.start",
		"params":  map[string]any{"name": ""},
	})
	checkProblem(t, res, http.StatusBadRequest, "Invalid request", "name", "name is required")

	// 2. Unknown fields in params
	res = request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "vm.stop",
		"params":  map[string]any{"name": "web-01", "extra": "field"},
	})
	checkProblem(t, res, http.StatusBadRequest, "Invalid request", "", "unknown field")

	// 3. Non-existent VM for commands
	res = request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "vm.start",
		"params":  map[string]any{"name": "nonexistent"},
	})
	checkProblem(t, res, http.StatusNotFound, "Resource not found", "", "nonexistent")

	res = request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "vm.delete",
		"params":  map[string]any{"name": "nonexistent"},
	})
	checkProblem(t, res, http.StatusNotFound, "Resource not found", "", "nonexistent")

	// 4. Non-existent VM for REST
	res = request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/resources/VirtualMachine/nonexistent:start", nil)
	checkProblem(t, res, http.StatusNotFound, "Resource not found", "", "nonexistent")

	// 5. Invalid operation for REST
	res = request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/resources/VirtualMachine/web-01", nil)
	checkProblem(t, res, http.StatusBadRequest, "Invalid operation", "", "missing operation suffix")

	res = request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/resources/VirtualMachine/web-01:reboot", nil)
	checkProblem(t, res, http.StatusBadRequest, "Unknown operation", "", "reboot")

	// 6. Invalid resource kind for REST
	res = request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/resources/Network/dmz:start", nil)
	checkProblem(t, res, http.StatusBadRequest, "Invalid resource kind", "", "Network")

	res = request(t, handler, http.MethodDelete, "/api/v1/workspaces/homelab/resources/Network/dmz", nil)
	checkProblem(t, res, http.StatusBadRequest, "Invalid resource kind", "", "Network")
}
