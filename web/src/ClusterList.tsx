import { useState, type FormEvent } from "react";
import {
  clusterDiscover,
  ProblemError,
  updateProxmoxCluster,
  type DiscoveredGuest,
  type ProxmoxCluster,
  type ProblemDetails,
} from "./api";
import Banner from "./components/Banner";
import Button from "./components/Button";
import Modal from "./components/Modal";
import StatusBadge from "./components/StatusBadge";

interface ClusterListProps {
  clusters: ProxmoxCluster[];
  workspace: string;
  onRefresh: () => void | Promise<void>;
}

function splitLines(value: string): string[] {
  return value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export default function ClusterList({
  clusters,
  workspace,
  onRefresh,
}: ClusterListProps) {
  const [editing, setEditing] = useState<ProxmoxCluster | null>(null);
  const [endpoints, setEndpoints] = useState("");
  const [nodes, setNodes] = useState("");
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState<ProblemDetails | null>(null);
  const [discoverCluster, setDiscoverCluster] = useState<ProxmoxCluster | null>(null);
  const [discovering, setDiscovering] = useState(false);
  const [discoverResults, setDiscoverResults] = useState<DiscoveredGuest[] | null>(null);
  const [discoverProblem, setDiscoverProblem] = useState<ProblemDetails | null>(null);

  function beginEdit(cluster: ProxmoxCluster) {
    setProblem(null);
    setEndpoints((cluster.endpoints ?? []).join("\n"));
    setNodes((cluster.nodes ?? []).join("\n"));
    setEditing(cluster);
  }

  async function handleSave(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editing) return;
    setSaving(true);
    setProblem(null);
    try {
      await updateProxmoxCluster(workspace, editing.name, {
        endpoints: splitLines(endpoints),
        nodes: splitLines(nodes),
      });
      setEditing(null);
      await onRefresh();
    } catch (error) {
		setProblem(
			error instanceof ProblemError
				? error.problem
				: { type: "about:blank", title: "Error", status: 500, detail: error instanceof Error ? error.message : String(error) },
		);
    } finally {
      setSaving(false);
    }
  }

  async function handleDiscover(cluster: ProxmoxCluster) {
    setDiscoverProblem(null);
    setDiscoverResults(null);
    setDiscoverCluster(cluster);
    setDiscovering(true);
    try {
      const guests = await clusterDiscover(workspace, cluster.name);
      setDiscoverResults(guests);
    } catch (error) {
      setDiscoverProblem(
        error instanceof ProblemError
          ? error.problem
          : { type: "about:blank", title: "Error", status: 500, detail: error instanceof Error ? error.message : String(error) },
      );
    } finally {
      setDiscovering(false);
    }
  }

  function closeDiscover() {
    setDiscoverCluster(null);
    setDiscoverResults(null);
    setDiscoverProblem(null);
  }

  function classificationLabel(classified: string): string {
    if (classified === "managed") return "Managed by nodr";
    if (classified === "discovered (other tool?)") return "Tagged by another tool";
    return "Undiscovered";
  }

  function classificationModifier(classified: string): string {
    if (classified === "managed") return "status-managed";
    if (classified === "discovered (other tool?)") return "status-other-tool";
    return "status-undiscovered";
  }

  return (
    <section aria-labelledby="cluster-list-heading">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Proxmox VE</p>
          <h2 id="cluster-list-heading">Clusters</h2>
        </div>
      </div>
      {clusters.length === 0 ? (
        <p className="empty-state">No Proxmox clusters are connected.</p>
      ) : (
        <div className="table-wrap">
          <table>
            <thead><tr><th scope="col">Name</th><th scope="col">Endpoints</th><th scope="col">Nodes</th><th scope="col">Actions</th></tr></thead>
            <tbody>
              {clusters.map((cluster) => (
                <tr key={cluster.name}>
                  <td>{cluster.name}</td>
                  <td>{(cluster.endpoints ?? []).length === 0 ? "—" : (cluster.endpoints ?? []).map((endpoint, index) => {
                    try { const url = new URL(endpoint); return url.protocol === "http:" || url.protocol === "https:" ? <span key={endpoint}>{index > 0 ? ", " : ""}<a href={endpoint} target="_blank" rel="noreferrer">{endpoint}</a></span> : <span key={endpoint}>{index > 0 ? ", " : ""}{endpoint}</span>; }
                    catch { return <span key={endpoint}>{index > 0 ? ", " : ""}{endpoint}</span>; }
                  })}</td>
                  <td>{(cluster.nodes ?? []).join(", ") || "—"}</td>
                  <td className="actions-cell"><Button variant="secondary" size="small" onClick={() => beginEdit(cluster)}>Edit</Button><Button variant="secondary" size="small" onClick={() => void handleDiscover(cluster)}>Discover</Button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Modal open={editing !== null} onClose={() => setEditing(null)} closeDisabled={saving} titleId="edit-cluster-heading">
        <h3 id="edit-cluster-heading">Edit {editing?.name}</h3>
        {problem ? <Banner variant="error">{problem.detail}</Banner> : null}
        <form onSubmit={handleSave}>
          <div className="field">
            <label htmlFor="cluster-endpoints">Endpoints</label>
            <textarea id="cluster-endpoints" value={endpoints} onChange={(event) => setEndpoints(event.target.value)} rows={4} required />
            <small>One HTTPS API URL per line.</small>
          </div>
          <div className="field">
            <label htmlFor="cluster-nodes">Nodes</label>
            <textarea id="cluster-nodes" value={nodes} onChange={(event) => setNodes(event.target.value)} rows={4} />
            <small>One node name per line.</small>
          </div>
          <div className="modal-actions"><Button variant="secondary" onClick={() => setEditing(null)} disabled={saving}>Cancel</Button><Button type="submit" disabled={saving}>{saving ? "Saving…" : "Save"}</Button></div>
        </form>
      </Modal>
      <Modal open={discoverCluster !== null} onClose={closeDiscover} closeDisabled={discovering} titleId="discover-guests-heading">
        <h3 id="discover-guests-heading">Guests on {discoverCluster?.name}</h3>
        <p>
          Read-only: live QEMU guests classified against this workspace&rsquo;s
          intent. Undiscovered guests are not added automatically &mdash;
          write a <code>VirtualMachine</code> document for any you want nodr
          to manage.
        </p>
        {discoverProblem ? <Banner variant="error">{discoverProblem.detail}</Banner> : null}
        {discovering ? <p className="status" role="status">Discovering…</p> : null}
        {!discovering && discoverResults && discoverResults.length === 0 ? (
          <p className="empty-state">No live QEMU guests were found on this cluster.</p>
        ) : null}
        {!discovering && discoverResults && discoverResults.length > 0 ? (
          <div className="table-wrap">
            <table>
              <thead><tr><th scope="col">VMID</th><th scope="col">Name</th><th scope="col">Node</th><th scope="col">Status</th><th scope="col">Classification</th></tr></thead>
              <tbody>
                {discoverResults.map((guest) => (
                  <tr key={guest.vmid}>
                    <td>{guest.vmid}</td>
                    <td>{guest.name}</td>
                    <td>{guest.node}</td>
                    <td><StatusBadge powerState={guest.status} /></td>
                    <td><span className={`status-badge ${classificationModifier(guest.classified)}`}>{classificationLabel(guest.classified)}</span></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
        <div className="modal-actions"><Button variant="secondary" onClick={closeDiscover} disabled={discovering}>Close</Button></div>
      </Modal>
    </section>
  );
}
