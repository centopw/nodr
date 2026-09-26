import { useEffect, useState } from "react";
import {
  deleteVM,
  ProblemError,
  startVM,
  stopVM,
  type ProblemDetails,
  type VirtualMachine,
} from "./api";

export interface VMListProps {
  virtualMachines: VirtualMachine[];
  workspace?: string;
  onRefresh?: () => void | Promise<void>;
}

function renderStatusBadge(powerState: string) {
  const normalized = (powerState || "").toLowerCase();
  let badgeClass = "status-badge status-unmanaged";
  let label = powerState || "unmanaged";

  if (normalized === "running") {
    badgeClass = "status-badge status-running";
    label = "running";
  } else if (normalized === "stopped") {
    badgeClass = "status-badge status-stopped";
    label = "stopped";
  } else if (normalized === "unmanaged") {
    badgeClass = "status-badge status-unmanaged";
    label = "unmanaged";
  }

  return <span className={badgeClass}>{label}</span>;
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
    <div className="message message-error error-banner" role="alert">
      <div className="error-banner-content">
        <p>
          <strong>{problem.title || "Error"}:</strong> {problem.detail}
        </p>
        {problem.errors && problem.errors.length > 0 ? (
          <ul className="field-errors">
            {problem.errors.map((error, idx) => (
              <li key={idx}>{error.message}</li>
            ))}
          </ul>
        ) : null}
      </div>
      <button
        type="button"
        className="action-btn btn-secondary"
        onClick={() => setProblem(null)}
        aria-label="Dismiss error"
      >
        Dismiss
      </button>
    </div>
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
                  <td>{renderStatusBadge(vm.powerState)}</td>
                  <td>{vm.cluster}</td>
                  <td>{vm.node === "" ? "—" : vm.node}</td>
                  <td>{vm.vmid === 0 ? "—" : vm.vmid}</td>
                  <td>{vm.cpu}</td>
                  <td>{vm.memory}</td>
                  <td>{vm.addresses.length > 0 ? vm.addresses.join(", ") : "—"}</td>
                  <td>
                    <div className="actions-cell">
                      {isRunning ? (
                        <button
                          type="button"
                          className="action-btn btn-secondary"
                          onClick={() => handleStop(vm.name)}
                          disabled={isLoading || !workspace}
                        >
                          {currentAction === "stop" ? "Stopping…" : "Stop"}
                        </button>
                      ) : null}
                      {isStopped ? (
                        <button
                          type="button"
                          className="action-btn"
                          onClick={() => handleStart(vm.name)}
                          disabled={isLoading || !workspace}
                        >
                          {currentAction === "start" ? "Starting…" : "Start"}
                        </button>
                      ) : null}
                      <button
                        type="button"
                        className="action-btn btn-danger"
                        onClick={() => setConfirmDeleteName(vm.name)}
                        disabled={isLoading || !workspace}
                      >
                        {currentAction === "delete" ? "Deleting…" : "Delete"}
                      </button>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {confirmDeleteName ? (
        <div
          className="modal-backdrop"
          role="presentation"
          onClick={(e) => {
            if (e.target === e.currentTarget && !loadingMap[confirmDeleteName]) {
              setConfirmDeleteName(null);
            }
          }}
        >
          <div
            className="modal-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="confirm-delete-title"
          >
            <h3 id="confirm-delete-title">Confirm deletion</h3>
            <p>
              Delete {confirmDeleteName}? This removes the intent definition. The VM will be scheduled for destruction in the next plan.
            </p>
            <div className="modal-actions">
              <button
                type="button"
                className="button-secondary btn-secondary"
                onClick={() => setConfirmDeleteName(null)}
                disabled={Boolean(loadingMap[confirmDeleteName])}
              >
                Cancel
              </button>
              <button
                type="button"
                className="btn-danger"
                onClick={() => handleDelete(confirmDeleteName)}
                disabled={Boolean(loadingMap[confirmDeleteName])}
              >
                {loadingMap[confirmDeleteName] === "delete"
                  ? "Deleting…"
                  : "Delete"}
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </>
  );
}
