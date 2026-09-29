import { useEffect, useRef, useState } from "react";
import {
  getWorkspace,
  listResources,
  listWorkspaces,
  type NamedResource,
  type ProxmoxCluster,
  type VirtualMachine,
  type WorkspaceManifest,
} from "./api";
import Banner from "./components/Banner";
import ChangesPanel from "./ChangesPanel";
import ClusterConnectWizard from "./ClusterConnectWizard";
import ClusterList from "./ClusterList";
import NewVMForm from "./NewVMForm";
import Overview from "./Overview";
import VMList from "./VMList";
import WorkspaceRail from "./WorkspaceRail";

type Section = "overview" | "infrastructure" | "changes";
type InfrastructureView = "list" | "new";

interface AppData {
  workspace: string;
  manifest: WorkspaceManifest;
  virtualMachines: VirtualMachine[];
  clusters: ProxmoxCluster[];
  networks: NamedResource[];
  templates: NamedResource[];
}

export default function App() {
  const [section, setSection] = useState<Section>("infrastructure");
  const [infrastructureView, setInfrastructureView] = useState<InfrastructureView>("list");
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
    try { return window.localStorage.getItem("nodr.sidebar-collapsed") === "true"; } catch { return false; }
  });
  const [workspaces, setWorkspaces] = useState<string[]>([]);
  const [selectedWorkspace, setSelectedWorkspace] = useState<string | null>(null);
  const [data, setData] = useState<AppData | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const connectClusterRef = useRef<HTMLDivElement>(null);
  const shouldFocusConnectCluster = useRef(false);
  const [connectFocusToken, setConnectFocusToken] = useState(0);

  useEffect(() => {
    let cancelled = false;
    async function loadWorkspaces() {
      try {
        const items = await listWorkspaces();
        if (!items[0]) throw new Error("No workspace is available.");
        if (!cancelled) { setWorkspaces(items.map((item) => item.name)); setSelectedWorkspace((current) => current ?? items[0].name); }
      } catch (error) { if (!cancelled) setLoadError(error instanceof Error ? error.message : "Unable to load nodr."); }
      finally { if (!cancelled) setLoading(false); }
    }
    void loadWorkspaces();
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    try { window.localStorage.setItem("nodr.sidebar-collapsed", String(sidebarCollapsed)); } catch { /* Storage can be unavailable. */ }
  }, [sidebarCollapsed]);

  useEffect(() => {
    if (!selectedWorkspace) return;
    const workspace = selectedWorkspace;
    let cancelled = false;
    setLoading(true);
    setLoadError(null);
    async function load() {
      try {
        const [manifest, virtualMachines, clusters, networks, templates] = await Promise.all([getWorkspace(workspace), listResources<VirtualMachine>(workspace, "VirtualMachine"), listResources<ProxmoxCluster>(workspace, "ProxmoxCluster"), listResources<NamedResource>(workspace, "Network"), listResources<NamedResource>(workspace, "Template")]);
        if (!cancelled) setData({ workspace, manifest, virtualMachines, clusters, networks, templates });
      } catch (error) { if (!cancelled) setLoadError(error instanceof Error ? error.message : "Unable to load nodr."); }
      finally { if (!cancelled) setLoading(false); }
    }
    void load();
    return () => { cancelled = true; };
  }, [selectedWorkspace]);

  useEffect(() => {
    if (!shouldFocusConnectCluster.current || section !== "infrastructure" || infrastructureView !== "list") return;
    shouldFocusConnectCluster.current = false;
    connectClusterRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
    connectClusterRef.current?.focus();
  }, [infrastructureView, section, connectFocusToken]);

  async function refreshResources() {
    if (!data) return;
    const workspace = data.workspace;
    const [virtualMachines, clusters] = await Promise.all([listResources<VirtualMachine>(workspace, "VirtualMachine"), listResources<ProxmoxCluster>(workspace, "ProxmoxCluster")]);
    setData((current) => current?.workspace === workspace ? { ...current, virtualMachines, clusters } : current);
  }

  function showInfrastructure(view: InfrastructureView = "list") { setSuccess(null); setInfrastructureView(view); setSection("infrastructure"); }
  function showConnectCluster() { shouldFocusConnectCluster.current = true; setConnectFocusToken((token) => token + 1); showInfrastructure(); }

  if (loadError) return <main className="app-shell app-shell-centered"><Banner variant="error">{loadError}</Banner></main>;
  if (loading || !selectedWorkspace || !data) return <main className="app-shell app-shell-centered"><p className="status" role="status">Loading workspace…</p></main>;
  const environments = Object.keys(data.manifest.environments);

  const navItems: Array<{ id: Section; label: string; counts?: { virtualMachines: number; clusters: number } }> = [{ id: "overview", label: "Overview" }, { id: "infrastructure", label: "Infrastructure", counts: { virtualMachines: data.virtualMachines.length, clusters: data.clusters.length } }, { id: "changes", label: "Changes" }];
  return <main className={`app-shell${sidebarCollapsed ? " sidebar-is-collapsed" : ""}`}>
    <aside className="app-sidebar">
      <div className="sidebar-identity"><p className="eyebrow">Infrastructure</p><p className="product-name">nodr</p></div>
      <button className="sidebar-toggle" type="button" onClick={() => setSidebarCollapsed((current) => !current)} aria-expanded={!sidebarCollapsed} aria-label={sidebarCollapsed ? "Expand navigation" : "Collapse navigation"}>{sidebarCollapsed ? "Expand" : "Collapse"}</button>
      <nav className="app-nav" aria-label="Primary">{navItems.map((item) => <button className={section === item.id ? "nav-item is-selected" : "nav-item"} key={item.id} onClick={() => item.id === "infrastructure" ? showInfrastructure() : (setSuccess(null), setSection(item.id))} aria-current={section === item.id ? "page" : undefined} title={sidebarCollapsed ? item.label : undefined}><span>{item.label}</span>{item.counts ? <><span className="nav-count" aria-label={`${item.counts.virtualMachines} virtual machines`}>VM {item.counts.virtualMachines}</span><span className="nav-count" aria-label={`${item.counts.clusters} clusters`}>Clusters {item.counts.clusters}</span></> : null}</button>)}</nav>
    </aside>
    <div className="app-main">
      <header className="context-bar"><label className="workspace-picker"><span>Workspace</span><select value={data.workspace} onChange={(event) => { setSuccess(null); setSection("infrastructure"); setInfrastructureView("list"); setSelectedWorkspace(event.target.value); }}>{workspaces.map((workspace) => <option key={workspace}>{workspace}</option>)}</select></label><p className="breadcrumb" aria-label="Current location">{data.workspace} <span aria-hidden="true">/</span> {section === "infrastructure" && infrastructureView === "new" ? "Infrastructure / New VM" : navItems.find((item) => item.id === section)?.label}</p></header>
      <div className="app-content">
        {section === "overview" ? <Overview virtualMachines={data.virtualMachines} clusters={data.clusters} networks={data.networks} templates={data.templates} onCreateVM={() => showInfrastructure("new")} onConnectCluster={showConnectCluster} onPlanChanges={() => setSection("changes")} /> : null}
        {section === "changes" ? <ChangesPanel workspace={data.workspace} onApplied={() => void refreshResources()} onBack={() => showInfrastructure()} /> : null}
        {section === "infrastructure" && infrastructureView === "new" ? <NewVMForm workspace={data.workspace} environments={environments} clusters={data.clusters} templates={data.templates} networks={data.networks} onCreated={async (summary) => { setInfrastructureView("list"); await refreshResources(); setSuccess(summary); }} onCancel={() => setInfrastructureView("list")} /> : null}
        {section === "infrastructure" && infrastructureView === "list" ? <div className="infrastructure-layout"><div className="inventory-stack"><section aria-labelledby="vm-list-heading"><div className="section-heading"><div><p className="eyebrow">Infrastructure</p><h1 id="vm-list-heading">Virtual machines</h1></div></div>{success ? <Banner variant="success">{success}</Banner> : null}<VMList virtualMachines={data.virtualMachines} workspace={data.workspace} onRefresh={() => void refreshResources()} hasCluster={data.clusters.length > 0} onCreateVM={() => showInfrastructure("new")} onConnectCluster={showConnectCluster} /></section><ClusterList clusters={data.clusters} workspace={data.workspace} onRefresh={() => void refreshResources()} /><div className="connect-cluster-target" ref={connectClusterRef} tabIndex={-1}><ClusterConnectWizard workspace={data.workspace} onConnected={async (summary) => { await refreshResources(); setSuccess(summary); }} /></div></div><WorkspaceRail virtualMachines={data.virtualMachines} clusters={data.clusters} networks={data.networks} templates={data.templates} onCreateVM={() => showInfrastructure("new")} onConnectCluster={showConnectCluster} onPlanChanges={() => setSection("changes")} /></div> : null}
      </div>
    </div>
  </main>;
}
