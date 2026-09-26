import { useEffect, useState } from "react";
import {
  getWorkspace,
  listResources,
  listWorkspaces,
  type NamedResource,
  type VirtualMachine,
  type WorkspaceManifest,
} from "./api";
import ChangesPanel from "./ChangesPanel";
import NewVMForm from "./NewVMForm";
import VMList from "./VMList";

type View = "list" | "new" | "changes";

interface AppData {
  workspace: string;
  manifest: WorkspaceManifest;
  virtualMachines: VirtualMachine[];
  clusters: NamedResource[];
  networks: NamedResource[];
  templates: NamedResource[];
}

export default function App() {
  const [view, setView] = useState<View>("list");
  const [data, setData] = useState<AppData | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function load() {
      try {
        const workspaces = await listWorkspaces();
        const workspace = workspaces[0]?.name;
        if (!workspace) {
          throw new Error("No workspace is available.");
        }

        const [manifest, virtualMachines, clusters, networks, templates] =
          await Promise.all([
            getWorkspace(workspace),
            listResources<VirtualMachine>(workspace, "VirtualMachine"),
            listResources<NamedResource>(workspace, "ProxmoxCluster"),
            listResources<NamedResource>(workspace, "Network"),
            listResources<NamedResource>(workspace, "Template"),
          ]);

        if (!cancelled) {
          setData({
            workspace,
            manifest,
            virtualMachines,
            clusters,
            networks,
            templates,
          });
        }
      } catch (error) {
        if (!cancelled) {
          setLoadError(
            error instanceof Error ? error.message : "Unable to load nodr.",
          );
        }
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    }

    void load();
    return () => {
      cancelled = true;
    };
  }, []);

  async function refreshVMs() {
    if (!data) {
      return;
    }

    try {
      const virtualMachines = await listResources<VirtualMachine>(
        data.workspace,
        "VirtualMachine",
      );
      setData((current) =>
        current ? { ...current, virtualMachines } : current,
      );
    } catch {
      // Background refresh failure is non-fatal
    }
  }

  async function handleCreated(summary: string) {
    if (!data) {
      return;
    }

    setView("list");

    try {
      await refreshVMs();
      setSuccess(summary);
    } catch {
      setSuccess(
        `${summary} The list could not be refreshed — reload the page.`,
      );
    }
  }

  async function handleApplied() {
    await refreshVMs();
  }

  if (loading) {
    return (
      <main className="app-shell">
        <p className="status" role="status">
          Loading virtual machines…
        </p>
      </main>
    );
  }

  if (loadError || !data) {
    return (
      <main className="app-shell">
        <div className="message message-error" role="alert">
          {loadError ?? "Unable to load nodr."}
        </div>
      </main>
    );
  }

  const environments = Object.keys(data.manifest.environments);

  return (
    <main className="app-shell">
      <header className="app-header">
        <div>
          <p className="eyebrow">Workspace: {data.workspace}</p>
          <h1>nodr</h1>
        </div>
      </header>

      {view === "list" ? (
        <section aria-labelledby="vm-list-heading">
          <div className="section-heading">
            <div>
              <p className="eyebrow">Infrastructure</p>
              <h2 id="vm-list-heading">Virtual machines</h2>
            </div>
            <div className="section-actions">
              <button
                type="button"
                className="button-secondary"
                onClick={() => {
                  setSuccess(null);
                  setView("changes");
                }}
              >
                Changes
              </button>
              <button
                type="button"
                onClick={() => {
                  setSuccess(null);
                  setView("new");
                }}
              >
                New VM
              </button>
            </div>
          </div>

          {success ? (
            <div className="message message-success" role="status">
              {success}
            </div>
          ) : null}

          <VMList
            virtualMachines={data.virtualMachines}
            workspace={data.workspace}
            onRefresh={refreshVMs}
          />
        </section>
      ) : view === "new" ? (
        <NewVMForm
          workspace={data.workspace}
          environments={environments}
          clusters={data.clusters}
          templates={data.templates}
          networks={data.networks}
          onCreated={handleCreated}
          onCancel={() => setView("list")}
        />
      ) : (
        <ChangesPanel
          workspace={data.workspace}
          onApplied={handleApplied}
          onBack={() => setView("list")}
        />
      )}
    </main>
  );
}
