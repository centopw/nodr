import type { VirtualMachine } from "./api";

interface VMListProps {
  virtualMachines: VirtualMachine[];
}


export default function VMList({ virtualMachines }: VMListProps) {
  if (virtualMachines.length === 0) {
    return <p className="empty-state">No virtual machines found.</p>;
  }

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th scope="col">Name</th>
            <th scope="col">Cluster</th>
            <th scope="col">Node</th>
            <th scope="col">Guest ID</th>
            <th scope="col">CPU</th>
            <th scope="col">Memory</th>
            <th scope="col">Address</th>
          </tr>
        </thead>
        <tbody>
          {virtualMachines.map((vm) => (
            <tr key={`${vm.environment}/${vm.name}`}>
              <td>{vm.name}</td>
              <td>{vm.cluster}</td>
              <td>{vm.node === "" ? "—" : vm.node}</td>
              <td>{vm.vmid === 0 ? "—" : vm.vmid}</td>
              <td>{vm.cpu}</td>
              <td>{vm.memory}</td>
              <td>{vm.addresses.length > 0 ? vm.addresses.join(", ") : "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
