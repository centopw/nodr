import type { NamedResource, ProxmoxCluster, VirtualMachine } from "./api";
import Button from "./components/Button";

interface WorkspaceRailProps {
  virtualMachines: VirtualMachine[];
  clusters: ProxmoxCluster[];
  networks: NamedResource[];
  templates: NamedResource[];
  onCreateVM: () => void;
  onConnectCluster: () => void;
  onPlanChanges: () => void;
}

export default function WorkspaceRail({
  virtualMachines,
  clusters,
  networks,
  templates,
  onCreateVM,
  onConnectCluster,
  onPlanChanges,
}: WorkspaceRailProps) {
  const hasClusters = clusters.length > 0;
  const metrics = [
    ["Virtual machines", virtualMachines.length],
    ["Clusters", clusters.length],
    ["Networks", networks.length],
    ["Templates", templates.length],
  ] as const;

  return (
    <aside className="workspace-rail" aria-label="Workspace summary">
      <div>
        <p className="eyebrow">Workspace</p>
        <h2>Inventory</h2>
      </div>
      <dl className="rail-totals">
        {metrics.map(([label, value]) => (
          <div key={label}>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
      <div className="rail-actions">
        {hasClusters ? (
          <Button onClick={onCreateVM}>Create VM</Button>
        ) : (
          <Button onClick={onConnectCluster}>Connect cluster</Button>
        )}
        <Button variant="secondary" onClick={onPlanChanges}>
          Plan changes
        </Button>
      </div>
    </aside>
  );
}
