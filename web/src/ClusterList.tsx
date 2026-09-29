import { useState, type FormEvent } from "react";
import {
  ProblemError,
  updateProxmoxCluster,
  type ProxmoxCluster,
  type ProblemDetails,
} from "./api";
import Banner from "./components/Banner";
import Button from "./components/Button";
import Modal from "./components/Modal";

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

  function beginEdit(cluster: ProxmoxCluster) {
    setProblem(null);
    setEndpoints(cluster.endpoints.join("\n"));
    setNodes(cluster.nodes.join("\n"));
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
                  <td>{cluster.endpoints.map((endpoint, index) => {
                    try { const url = new URL(endpoint); return url.protocol === "http:" || url.protocol === "https:" ? <span key={endpoint}>{index > 0 ? ", " : ""}<a href={endpoint} target="_blank" rel="noreferrer">{endpoint}</a></span> : <span key={endpoint}>{index > 0 ? ", " : ""}{endpoint}</span>; }
                    catch { return <span key={endpoint}>{index > 0 ? ", " : ""}{endpoint}</span>; }
                  })}</td>
                  <td>{cluster.nodes.join(", ") || "—"}</td>
                  <td><Button variant="secondary" size="small" onClick={() => beginEdit(cluster)}>Edit</Button></td>
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
    </section>
  );
}
