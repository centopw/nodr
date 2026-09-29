const API_BASE = "/api/v1";

export type ResourceKind =
  | "VirtualMachine"
  | "ProxmoxCluster"
  | "Network"
  | "Template";

export interface WorkspaceSummary {
  name: string;
}

export interface WorkspaceManifest {
  name: string;
  environments: Record<string, { vmidRange: [number, number] }>;
}

export interface NamedResource {
  kind: "ProxmoxCluster" | "Network" | "Template";
  name: string;
}

export interface VirtualMachine {
  kind: "VirtualMachine";
  name: string;
  cluster: string;
  node: string;
  vmid: number;
  cpu: number;
  memory: string;
  environment: string;
  addresses: string[];
  powerState: string;
}

export interface ProxmoxCluster {
	kind: "ProxmoxCluster";
	name: string;
	endpoints: string[];
	nodes: string[];
}

export interface ClusterConnectParams {
	cluster: string;
	endpoint: string;
	node: string;
	adminUsername: string;
	adminPassword: string;
}

export interface ClusterConnectProbeResult {
	fingerprint: string;
}

export interface UpdateProxmoxClusterParams {
	endpoints: string[];
	nodes: string[];
}

export interface ClusterConnectResult {
	cluster: string;
	endpoint: string;
	node: string;
	fingerprint: string;
	credentialsRef: string;
	tokenId: string;
	intentPath: string;
}


export type VMSize = "S" | "M" | "L";

export interface CreateVMParams {
  name: string;
  environment: string;
  cluster: string;
  template: string;
  network: string;
  storage: string;
  size: VMSize;
}

export interface CreateVMResult {
  name: string;
  cluster: string;
  node: string;
  vmid: number;
  cpu: number;
  memory: string;
  addresses: string[];
  macs: string[];
  summary: string;
}

export interface FieldError {
  path: string;
  message: string;
}

export interface ProblemDetails {
  type: string;
  title: string;
  status: number;
  detail: string;
  errors?: FieldError[];
  units?: ApplyUnitOutcome[];
}

export class ProblemError extends Error {
  readonly problem: ProblemDetails;

  constructor(problem: ProblemDetails) {
    super(problem.detail);
    this.name = "ProblemError";
    this.problem = problem;
  }
}

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (response.status === 401) {
    window.location.href = "/login";
    throw new Error("session expired, redirecting to /login");
  }
  const body: unknown = await response.json();
  if (!response.ok) {
    throw new ProblemError(body as ProblemDetails);
  }
  return body as T;
}

export async function listWorkspaces(): Promise<WorkspaceSummary[]> {
  const response = await fetchJSON<{ items: WorkspaceSummary[] }>(
    `${API_BASE}/workspaces`,
  );
  return response.items;
}

export function getWorkspace(name: string): Promise<WorkspaceManifest> {
  return fetchJSON(`${API_BASE}/workspaces/${encodeURIComponent(name)}`);
}

export async function listResources<T>(
  workspace: string,
  kind: ResourceKind,
): Promise<T[]> {
  const response = await fetchJSON<{ items: T[] }>(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/resources?kind=${encodeURIComponent(kind)}`,
  );
  return response.items;
}

export function createVM(
  workspace: string,
  params: CreateVMParams,
): Promise<CreateVMResult> {
  return fetchJSON(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/commands`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ command: "vm.create", params }),
    },
  );
}

export function startVM(
  workspace: string,
  name: string,
): Promise<{ name: string; powerState: string }> {
  return fetchJSON(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/commands`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ command: "vm.start", params: { name } }),
    },
  );
}

export function stopVM(
  workspace: string,
  name: string,
): Promise<{ name: string; powerState: string }> {
  return fetchJSON(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/commands`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ command: "vm.stop", params: { name } }),
    },
  );
}

export function deleteVM(
  workspace: string,
  name: string,
): Promise<{ name: string; deleted: boolean }> {
  return fetchJSON(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/commands`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ command: "vm.delete", params: { name } }),
    },
  );
}

export function clusterConnectProbe(
	workspace: string,
	params: ClusterConnectParams,
): Promise<ClusterConnectProbeResult> {
	return fetchJSON(
		`${API_BASE}/workspaces/${encodeURIComponent(workspace)}/commands`,
		{
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ command: "cluster.connect.probe", params }),
		},
	);
}

export function clusterConnect(
	workspace: string,
	params: ClusterConnectParams & { fingerprint: string },
): Promise<ClusterConnectResult> {
	return fetchJSON(
		`${API_BASE}/workspaces/${encodeURIComponent(workspace)}/commands`,
		{
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ command: "cluster.connect", params }),
		},
	);
}

export function updateProxmoxCluster(
	workspace: string,
	name: string,
	params: UpdateProxmoxClusterParams,
): Promise<ProxmoxCluster> {
	return fetchJSON(
		`${API_BASE}/workspaces/${encodeURIComponent(workspace)}/resources/ProxmoxCluster/${encodeURIComponent(name)}`,
		{
			method: "PUT",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify(params),
		},
	);
}

export type DiscoveredGuestStatus =
  | "managed"
  | "discovered"
  | "discovered (other tool?)";

export interface DiscoveredGuest {
  vmid: number;
  name: string;
  node: string;
  status: string;
  cpu: number;
  memory: string;
  disk: string;
  tags?: string[];
  description?: string;
  classified: DiscoveredGuestStatus;
}

export function clusterDiscover(
  workspace: string,
  cluster: string,
): Promise<DiscoveredGuest[]> {
  return fetchJSON(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/commands`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        command: "cluster.discover",
        params: { cluster },
      }),
    },
  );
}

export type ChangeAction =
  | "create"
  | "update"
  | "replace"
  | "delete"
  | "read"
  | "forget";

export interface PlanChange {
  address: string;
  action: ChangeAction;
}

export interface PlanUnitSummary {
  create: number;
  update: number;
  replace: number;
  delete: number;
}

export interface PlanUnit {
  dir: string;
  hasChanges: boolean;
  summary: PlanUnitSummary;
  destructive: boolean;
  changes: PlanChange[];
}

export interface PlanResult {
  planId: string;
  units: PlanUnit[];
  hasChanges: boolean;
  hasDestructiveChanges: boolean;
}

export type ApplyOutcome = "applied" | "failed" | "not applied";

export interface ApplyUnitOutcome {
  dir: string;
  outcome: ApplyOutcome;
}

export interface ApplyResult {
  units: ApplyUnitOutcome[];
}

export function planChanges(workspace: string): Promise<PlanResult> {
  return fetchJSON(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/commands`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ command: "workspace.plan", params: {} }),
    },
  );
}

export function applyChanges(
  workspace: string,
  planId: string,
  allowDestroy: boolean,
): Promise<ApplyResult> {
  return fetchJSON(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/commands`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        command: "workspace.apply",
        params: { planId, allowDestroy },
      }),
    },
  );
}
