import { useEffect, useState } from "react";
import {
  deleteVM,
  ProblemError,
  startVM,
  stopVM,
  type ProblemDetails,
  type VirtualMachine,
} from "./api";
import Banner from "./components/Banner";
import Button from "./components/Button";
import FieldErrorList from "./components/FieldErrorList";
import Modal from "./components/Modal";
import StatusBadge from "./components/StatusBadge";

export interface VMListProps {
  virtualMachines: VirtualMachine[];
  workspace?: string;
  onRefresh?: () => void | Promise<void>;
}

export default function VMList({
  virtualMachines,
  workspace,
  onRefresh,
}: VMListProps) {
  const [loadingMap, setLoadingMap] = useState<
    Record<string, "start" | "stop" | "delete">
  >({});
  const [confirmDeleteName, setConfirmDeleteName] = useState<string | null>(null);
  const [problem, setProblem] = useState<ProblemDetails | null>(null);

  useEffect(() => {
    if (!confirmDeleteName) {
      return;
    }
    function handleKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape" && !loadingMap[confirmDeleteName!]) {
        setConfirmDeleteName(null);
      }
    }
    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [confirmDeleteName, loadingMap]);

  async function handleStart(name: string) {
    if (!workspace) return;
    setProblem(null);
    setLoadingMap((prev) => ({ ...prev, [name]: "start" }));
    try {
      await startVM(workspace, name);
      await onRefresh?.();
    } catch (err: unknown) {
      if (err instanceof ProblemError) {
        setProblem(err.problem);
      } else {
        setProblem({
          type: "about:blank",
          title: "Error",
          status: 500,
          detail:
            err instanceof Error
              ? err.message
              : "An unexpected error occurred while starting the virtual machine.",
        });
      }
    } finally {
      setLoadingMap((prev) => {
        const next = { ...prev };
        delete next[name];
        return next;
      });
    }
  }

  async function handleStop(name: string) {
    if (!workspace) return;
    setProblem(null);
    setLoadingMap((prev) => ({ ...prev, [name]: "stop" }));
    try {
      await stopVM(workspace, name);
      await onRefresh?.();
    } catch (err: unknown) {
      if (err instanceof ProblemError) {
        setProblem(err.problem);
      } else {
        setProblem({
          type: "about:blank",
          title: "Error",
          status: 500,
          detail:
            err instanceof Error
              ? err.message
              : "An unexpected error occurred while stopping the virtual machine.",
        });
      }
    } finally {
      setLoadingMap((prev) => {
        const next = { ...prev };
        delete next[name];
        return next;
      });
    }
  }

  async function handleDelete(name: string) {
    if (!workspace) return;
    setProblem(null);
    setLoadingMap((prev) => ({ ...prev, [name]: "delete" }));
    try {
      await deleteVM(workspace, name);
      setConfirmDeleteName(null);
      await onRefresh?.();
    } catch (err: unknown) {
      setConfirmDeleteName(null);
      if (err instanceof ProblemError) {
        setProblem(err.problem);
      } else {
        setProblem({
          type: "about:blank",
          title: "Error",
          status: 500,
          detail:
            err instanceof Error
              ? err.message
              : "An unexpected error occurred while deleting the virtual machine.",
        });
      }
    } finally {
      setLoadingMap((prev) => {
        const next = { ...prev };
        delete next[name];
        return next;
      });
    }
  }

  const errorBanner = problem ? (
    <Banner variant="error" className="error-banner">
      <div className="error-banner-content">
        <p>
          <strong>{problem.title || "Error"}:</strong> {problem.detail}
        </p>
        <FieldErrorList errors={problem.errors ?? []} />
      </div>
      <Button
        variant="secondary"
        size="small"
        onClick={() => setProblem(null)}
        aria-label="Dismiss error"
      >
        Dismiss
      </Button>
    </Banner>
  ) : null;

  if (virtualMachines.length === 0) {
    return (
      <>
        {errorBanner}
        <p className="empty-state">No virtual machines found.</p>
      </>
    );
  }

  return (
    <>
      {errorBanner}
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Status</th>
              <th scope="col">Cluster</th>
              <th scope="col">Node</th>
              <th scope="col">Guest ID</th>
              <th scope="col">CPU</th>
              <th scope="col">Memory</th>
              <th scope="col">Address</th>
              <th scope="col">Actions</th>
            </tr>
          </thead>
          <tbody>
            {virtualMachines.map((vm) => {
              const isRunning = vm.powerState === "running";
              const isStopped = vm.powerState === "stopped";
              const currentAction = loadingMap[vm.name];
              const isLoading = Boolean(currentAction);

              return (
                <tr key={`${vm.environment}/${vm.name}`}>
                  <td>{vm.name}</td>
                  <td><StatusBadge powerState={vm.powerState} /></td>
                  <td>{vm.cluster}</td>
                  <td>{vm.node === "" ? "—" : vm.node}</td>
                  <td>{vm.vmid === 0 ? "—" : vm.vmid}</td>
                  <td>{vm.cpu}</td>
                  <td>{vm.memory}</td>
                  <td>{vm.addresses.length > 0 ? vm.addresses.join(", ") : "—"}</td>
                  <td>
                    <div className="actions-cell">
                      {isRunning ? (
                        <Button
                          variant="secondary"
                          size="small"
                          onClick={() => handleStop(vm.name)}
                          disabled={isLoading || !workspace}
                        >
                          {currentAction === "stop" ? "Stopping…" : "Stop"}
                        </Button>
                      ) : null}
                      {isStopped ? (
                        <Button
                          size="small"
                          onClick={() => handleStart(vm.name)}
                          disabled={isLoading || !workspace}
                        >
                          {currentAction === "start" ? "Starting…" : "Start"}
                        </Button>
                      ) : null}
                      <Button
                        variant="danger"
                        size="small"
                        onClick={() => setConfirmDeleteName(vm.name)}
                        disabled={isLoading || !workspace}
                      >
                        {currentAction === "delete" ? "Deleting…" : "Delete"}
                      </Button>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <Modal
        open={confirmDeleteName !== null}
        onClose={() => setConfirmDeleteName(null)}
        closeDisabled={
          confirmDeleteName !== null && Boolean(loadingMap[confirmDeleteName])
        }
        titleId="confirm-delete-title"
      >
        <h3 id="confirm-delete-title">Confirm deletion</h3>
        <p>
          Delete {confirmDeleteName}? This removes the intent definition. The
          VM will be scheduled for destruction in the next plan.
        </p>
        <div className="modal-actions">
          <Button
            variant="secondary"
            onClick={() => setConfirmDeleteName(null)}
            disabled={Boolean(confirmDeleteName && loadingMap[confirmDeleteName])}
          >
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={() => confirmDeleteName && handleDelete(confirmDeleteName)}
            disabled={Boolean(confirmDeleteName && loadingMap[confirmDeleteName])}
          >
            {confirmDeleteName && loadingMap[confirmDeleteName] === "delete"
              ? "Deleting…"
              : "Delete"}
          </Button>
        </div>
      </Modal>
    </>
  );
}
