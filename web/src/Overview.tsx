import type { NamedResource, ProxmoxCluster, VirtualMachine } from "./api";
import Button from "./components/Button";

interface OverviewProps {
  virtualMachines: VirtualMachine[];
  clusters: ProxmoxCluster[];
  networks: NamedResource[];
  templates: NamedResource[];
  onCreateVM: () => void;
  onConnectCluster: () => void;
  onPlanChanges: () => void;
}

interface InventoryMetric {
  label: string;
  value: number;
  detail?: string;
}

export default function Overview({
  virtualMachines,
  clusters,
  networks,
  templates,
  onCreateVM,
  onConnectCluster,
  onPlanChanges,
}: OverviewProps) {
  const metrics: InventoryMetric[] = [
    { label: "Virtual machines", value: virtualMachines.length },
    { label: "Clusters", value: clusters.length },
    { label: "Networks", value: networks.length },
    { label: "Templates", value: templates.length },
  ];

  return <section aria-labelledby="overview-heading">
    <div className="section-heading overview-heading"><div><p className="eyebrow">Workspace inventory</p><h1 id="overview-heading">Overview</h1></div></div>
    <div className="inventory-grid">{metrics.map((metric) => <article className="inventory-card" key={metric.label}><p className="inventory-label">{metric.label}</p><p className="inventory-value">{metric.value}</p></article>)}</div>
    <section className="overview-actions" aria-labelledby="inventory-navigation-heading"><p className="eyebrow">Navigate</p><h2 id="inventory-navigation-heading">Inventory</h2><div className="overview-nav"><Button onClick={onCreateVM}>Create VM</Button><Button variant="secondary" onClick={onConnectCluster}>Connect cluster</Button><Button variant="secondary" onClick={onPlanChanges}>Plan changes</Button></div></section>
  </section>;
}
