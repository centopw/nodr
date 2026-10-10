const API_BASE = "/api/v1";

export type ResourceKind =
  | "VirtualMachine"
  | "ProxmoxCluster"
  | "Network"
  | "Template";

export interface WorkspaceSummary {
  name: string;
  // Ledger-mode fields. Present only when the server runs in GitHub ledger
  // mode; ledger mode is detected by the presence of headSha.
  owner?: string;
  repository?: string;
  headSha?: string;
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
	endpoints?: string[];
	nodes?: string[];
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

let csrfToken: string | null = null;

function setCSRFToken(token: string | null) {
  csrfToken = token;
}

async function fetchJSONResponse<T>(
  url: string,
  init?: RequestInit,
): Promise<{ body: T; headers: Headers }> {
  const method = (init?.method ?? "GET").toUpperCase();
  const headers = new Headers(init?.headers);
  if (method !== "GET" && method !== "HEAD" && csrfToken) {
    headers.set("X-CSRF-Token", csrfToken);
  }
  const response = await fetch(url, { ...init, headers });
  if (response.status === 401) {
    setCSRFToken(null);
    throw new Error("your session has expired, please sign in again");
  }
  const text = await response.text();
  const body: unknown = text ? JSON.parse(text) : undefined;
  if (!response.ok) {
    throw new ProblemError(body as ProblemDetails);
  }
  return { body: body as T, headers: response.headers };
}

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const { body } = await fetchJSONResponse<T>(url, init);
  return body;
}

// Default draft SHA per ledger workspace, registered by the app shell when a
// workspace reports headSha. Legacy workspaces never appear here.
const ledgerHeads = new Map<string, string>();

export function registerLedgerHead(workspace: string, headSha: string): void {
  ledgerHeads.set(workspace, headSha);
}

export function isLedgerWorkspace(workspace: string): boolean {
  return ledgerHeads.has(workspace);
}

export interface SetupStatus {
  initialized: boolean;
}

export interface SetupParams {
  token: string;
  workspaceName: string;
  username: string;
  password: string;
}

export interface LoginParams {
  username: string;
  password: string;
}

export interface SessionInfo {
  username: string;
  csrfToken: string;
}

export function getSetupStatus(): Promise<SetupStatus> {
  return fetchJSON(`${API_BASE}/setup/status`);
}

interface SetupProblemBody {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  "invalid-params"?: Array<{ name: string; reason: string }>;
}

// The setup handler reports field errors as RFC 7807 invalid-params; map each
// onto the errors[].path shape the rest of the API uses.
function setupProblem(body: SetupProblemBody): ProblemDetails {
  const errors: FieldError[] = (body["invalid-params"] ?? []).map((param) => ({
    path: `params.${param.name}`,
    message: param.reason,
  }));
  return {
    type: body.type ?? "about:blank",
    title: body.title ?? "Setup failed",
    status: body.status ?? 0,
    detail: body.detail ?? "Check the highlighted fields and try again.",
    errors: errors.length > 0 ? errors : undefined,
  };
}

export async function setup(params: SetupParams): Promise<void> {
  let response: { csrfToken: string };
  try {
    response = await fetchJSON<{ csrfToken: string }>(
      `${API_BASE}/setup`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(params),
      },
    );
  } catch (error) {
    if (error instanceof ProblemError) {
      throw new ProblemError(setupProblem(error.problem as unknown as SetupProblemBody));
    }
    throw error;
  }
  setCSRFToken(response.csrfToken);
}

export async function login(params: LoginParams): Promise<void> {
  const response = await fetchJSON<{ csrfToken: string }>(
    `${API_BASE}/auth/login`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(params),
    },
  );
  setCSRFToken(response.csrfToken);
}

export async function logout(): Promise<void> {
  await fetchJSON(`${API_BASE}/auth/logout`, { method: "POST" });
  setCSRFToken(null);
}

export async function getSession(): Promise<SessionInfo> {
  const session = await fetchJSON<SessionInfo>(`${API_BASE}/auth/session`);
  setCSRFToken(session.csrfToken);
  return session;
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

// ---------------------------------------------------------------------------
// GitHub ledger mode (M0). Routes and shapes follow the HTTP contract in
// docs/proposals/2026-10-09-m0-github-ledger-design.md.
// ---------------------------------------------------------------------------

export interface RepositoryRef {
  owner: string;
  name: string;
  defaultBranch: string;
  private: boolean;
}

export interface Installation {
  id: number;
  account: string;
  repositories: RepositoryRef[];
}

export async function listInstallations(): Promise<Installation[]> {
  const response = await fetchJSON<{ installations: Installation[] }>(
    `${API_BASE}/setup/github/installations`,
    { method: "POST" },
  );
  return response.installations;
}

export interface ConnectRepositoryParams {
  installationId: number;
  owner: string;
  repository: string;
}

export interface LedgerWorkspace {
  name: string;
  owner: string;
  repository: string;
  defaultBranch: string;
  headSha: string;
}

export function connectRepository(
  params: ConnectRepositoryParams,
): Promise<LedgerWorkspace> {
  return fetchJSON(`${API_BASE}/workspaces`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(params),
  });
}

export type ChangesetStatus = "open" | "planned" | "applied" | "abandoned";

export type OperationStatus = "queued" | "running" | "succeeded" | "failed";

export interface ChangesetSummary {
  id: string;
  status: ChangesetStatus;
  baseSha: string;
  draftSha: string;
  revision: number;
}

export type FileDiffStatus = "added" | "modified" | "deleted";

export interface FileDiff {
  path: string;
  status: FileDiffStatus;
  patch: string;
}

export interface ChangesetDetail extends ChangesetSummary {
  diff: FileDiff[];
}

export interface LedgerPlanChange {
  address: string;
  action: ChangeAction;
}

export interface LedgerPlanUnit {
  dir: string;
  create: number;
  update: number;
  replace: number;
  delete: number;
  changes: LedgerPlanChange[];
}

export interface LedgerPlanSummary {
  units: LedgerPlanUnit[];
  hasChanges: boolean;
  hasDestructive: boolean;
}

export interface Operation {
  id: string;
  kind: string;
  status: OperationStatus;
  changesetId: string;
  planSha: string;
  result: string;
  plan?: LedgerPlanSummary;
}

export interface LedgerCommandBody {
  command: string;
  params: unknown;
  changeset?: string;
}

export interface LedgerCommandResponse<R = unknown> {
  changeset: ChangesetSummary;
  diff: FileDiff[];
  result: R;
  // draftSha from the ETag response header, when the server sent one.
  etag: string | null;
}

export interface LedgerCommandOptions {
  // Default draft SHA the caller last observed (headSha or changeset draftSha).
  ifMatch: string;
}

// runLedgerCommand sends one command with a fresh Idempotency-Key per call.
// Callers generate one call per user action; a retried call must reuse the
// same key, so pass the response of a failed attempt back in rather than
// calling again blindly.
export async function runLedgerCommand<R = unknown>(
  workspace: string,
  body: LedgerCommandBody,
  options: LedgerCommandOptions,
): Promise<LedgerCommandResponse<R>> {
  const { body: payload, headers } = await fetchJSONResponse<{
    changeset: ChangesetSummary;
    diff: FileDiff[];
    result: R;
  }>(`${API_BASE}/workspaces/${encodeURIComponent(workspace)}/commands`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "If-Match": options.ifMatch,
      "Idempotency-Key": crypto.randomUUID(),
    },
    body: JSON.stringify(body),
  });
  return { ...payload, etag: headers.get("ETag") };
}

export async function listChangesets(
  workspace: string,
): Promise<ChangesetSummary[]> {
  const response = await fetchJSON<{ items: ChangesetSummary[] }>(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/changesets`,
  );
  return response.items;
}

export function getChangeset(
  workspace: string,
  id: string,
): Promise<ChangesetDetail> {
  return fetchJSON(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/changesets/${encodeURIComponent(id)}`,
  );
}

export async function planChangeset(
  workspace: string,
  id: string,
  ifMatch: string,
): Promise<Operation> {
  const response = await fetchJSON<{ operation: Operation }>(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/changesets/${encodeURIComponent(id)}:plan`,
    {
      method: "POST",
      headers: {
        "If-Match": ifMatch,
        "Idempotency-Key": crypto.randomUUID(),
      },
    },
  );
  return response.operation;
}

export async function applyChangeset(
  workspace: string,
  id: string,
  ifMatch: string,
  allowDestroy: boolean,
): Promise<Operation> {
  const response = await fetchJSON<{ operation: Operation }>(
    `${API_BASE}/workspaces/${encodeURIComponent(workspace)}/changesets/${encodeURIComponent(id)}:apply`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "If-Match": ifMatch,
        "Idempotency-Key": crypto.randomUUID(),
      },
      body: JSON.stringify({ allowDestroy }),
    },
  );
  return response.operation;
}

export function getOperation(id: string): Promise<Operation> {
  return fetchJSON(`${API_BASE}/operations/${encodeURIComponent(id)}`);
}

// subscribeEvents opens the server-sent event stream. The stream only signals
// that something changed; callers must re-fetch state rather than trust the
// payload. Returns a function that closes the stream.
export function subscribeEvents(onEvent: () => void): () => void {
  const source = new EventSource(`${API_BASE}/events`);
  source.onmessage = onEvent;
  for (const type of [
    "nodr.operation.started",
    "nodr.operation.succeeded",
    "nodr.operation.failed",
    "nodr.changeset.changed",
  ]) {
    source.addEventListener(type, onEvent);
  }
  return () => source.close();
}
