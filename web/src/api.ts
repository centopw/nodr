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
