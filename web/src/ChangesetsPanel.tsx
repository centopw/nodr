import { useCallback, useEffect, useState } from "react";
import {
  applyChangeset,
  getChangeset,
  getOperation,
  listChangesets,
  planChangeset,
  ProblemError,
  subscribeEvents,
  type ChangesetDetail,
  type ChangesetSummary,
  type LedgerPlanUnit,
  type Operation,
  type ProblemDetails,
} from "./api";
import Banner from "./components/Banner";
import Button from "./components/Button";
import FieldErrorList from "./components/FieldErrorList";
import StatusBadge from "./components/StatusBadge";

export interface ChangesetsPanelProps {
  workspace: string;
  onApplied?: () => void;
  onBack?: () => void;
}

const STALE_DRAFT_MESSAGE = "The draft changed - reload and plan again.";

function toProblem(error: unknown, fallback: string): ProblemDetails {
  if (error instanceof ProblemError) {
    return error.problem;
  }
  return {
    type: "about:blank",
    title: "Error",
    status: 500,
    detail: error instanceof Error ? error.message : fallback,
  };
}

function isStaleDraft(problem: ProblemDetails): boolean {
  return (
    problem.status === 412 ||
    problem.status === 409 ||
    problem.type.includes("PLAN_CHANGED")
  );
}

function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}

function sumUnits(units: LedgerPlanUnit[]) {
  return units.reduce(
    (totals, unit) => ({
      create: totals.create + unit.create,
      update: totals.update + unit.update,
      replace: totals.replace + unit.replace,
      delete: totals.delete + unit.delete,
    }),
    { create: 0, update: 0, replace: 0, delete: 0 },
  );
}

export default function ChangesetsPanel({
  workspace,
  onApplied,
  onBack,
}: ChangesetsPanelProps) {
  const [changesets, setChangesets] = useState<ChangesetSummary[] | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [detail, setDetail] = useState<ChangesetDetail | null>(null);
  const [operation, setOperation] = useState<Operation | null>(null);
  const [allowDestroy, setAllowDestroy] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetails | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [refreshToken, setRefreshToken] = useState(0);

  const refresh = useCallback(() => setRefreshToken((token) => token + 1), []);

  // Re-fetch on every server-sent invalidation; payloads are never trusted.
  useEffect(() => {
    return subscribeEvents(refresh);
  }, [refresh]);

  useEffect(() => {
    let cancelled = false;
    listChangesets(workspace)
      .then((items) => {
        if (!cancelled) {
          setChangesets(items);
          setLoadError(null);
        }
      })
      .catch((error: unknown) => {
        if (!cancelled) {
          setLoadError(toProblem(error, "Unable to load changesets.").detail);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [workspace, refreshToken]);

  useEffect(() => {
    if (!selectedId) {
      setDetail(null);
      return;
    }
    let cancelled = false;
    getChangeset(workspace, selectedId)
      .then((item) => {
        if (!cancelled) {
          setDetail(item);
        }
      })
      .catch((error: unknown) => {
        if (!cancelled) {
          setProblem(toProblem(error, "Unable to load the changeset."));
        }
      });
    return () => {
      cancelled = true;
    };
  }, [workspace, selectedId, refreshToken]);

  useEffect(() => {
    if (!operation) {
      return;
    }
    let cancelled = false;
    getOperation(operation.id)
      .then((latest) => {
        if (!cancelled) {
          setOperation(latest);
        }
      })
      .catch((error: unknown) => {
        if (!cancelled) {
          setProblem(toProblem(error, "Unable to load operation status."));
        }
      });
    return () => {
      cancelled = true;
    };
  }, [operation?.id, refreshToken]);

  async function handlePlan() {
    if (!detail) {
      return;
    }
    setBusy(true);
    setProblem(null);
    setOperation(null);
    setAllowDestroy(false);
    try {
      const op = await planChangeset(workspace, detail.id, detail.draftSha);
      setOperation(op);
      refresh();
    } catch (error) {
      const next = toProblem(error, "Unable to plan the changeset.");
      setProblem(
        isStaleDraft(next)
          ? { ...next, detail: STALE_DRAFT_MESSAGE }
          : next,
      );
    } finally {
      setBusy(false);
    }
  }

  async function handleApply() {
    if (!detail || !operation?.plan) {
      return;
    }
    setBusy(true);
    setProblem(null);
    try {
      const op = await applyChangeset(
        workspace,
        detail.id,
        detail.draftSha,
        allowDestroy,
      );
      setOperation(op);
      setAllowDestroy(false);
      onApplied?.();
      refresh();
    } catch (error) {
      const next = toProblem(error, "Unable to apply the changeset.");
      setProblem(
        isStaleDraft(next)
          ? { ...next, detail: STALE_DRAFT_MESSAGE }
          : next,
      );
    } finally {
      setBusy(false);
    }
  }

  const planned = detail?.status === "planned";
  const plan = operation?.plan;
  const destructive = plan?.hasDestructive ?? false;
  const applyDisabled =
    busy ||
    !detail ||
    detail.status !== "planned" ||
    (destructive && !allowDestroy);

  if (selectedId && detail) {
    const totals = plan ? sumUnits(plan.units) : null;
    return (
      <section aria-labelledby="changeset-heading">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Changes</p>
            <h2 id="changeset-heading">
              Changeset {shortId(detail.id)} <StatusBadge powerState={detail.status} />
            </h2>
            <p className="table-detail">
              Revision {detail.revision} · draft {shortId(detail.draftSha)}
            </p>
          </div>
          <Button variant="secondary" onClick={() => setSelectedId(null)}>
            Back to changesets
          </Button>
        </div>

        {problem ? (
          <Banner variant="error">
            <p>
              <strong>{problem.title || "Error"}:</strong>{" "}
              {isStaleDraft(problem) ? (
                <>
                  {problem.detail}{" "}
                  <Button
                    variant="secondary"
                    size="small"
                    onClick={() => {
                      setProblem(null);
                      setOperation(null);
                      refresh();
                    }}
                  >
                    Reload
                  </Button>
                </>
              ) : (
                problem.detail
              )}
            </p>
            <FieldErrorList errors={problem.errors ?? []} />
          </Banner>
        ) : null}

        <div className="changes-actions">
          <Button onClick={() => void handlePlan()} disabled={busy}>
            {busy && !planned ? "Planning…" : "Plan"}
          </Button>
          <Button onClick={() => void handleApply()} disabled={applyDisabled}>
            {busy && planned ? "Applying…" : "Apply"}
          </Button>
        </div>

        {operation ? (
          <Banner variant={operation.status === "failed" ? "error" : "success"} role="status">
            <p>
              Operation {shortId(operation.id)} ({operation.kind}):{" "}
              <strong>{operation.status}</strong>
            </p>
            {operation.status === "failed" && operation.result ? (
              <p>{operation.result}</p>
            ) : null}
          </Banner>
        ) : null}

        {destructive && planned ? (
          <Banner variant="warning">
            <p>
              <strong>Warning:</strong> This plan contains destructive changes
              that will replace or destroy resources.
            </p>
            <label className="checkbox-label">
              <input
                type="checkbox"
                checked={allowDestroy}
                onChange={(event) => setAllowDestroy(event.target.checked)}
              />
              <span>allow destroy</span>
            </label>
          </Banner>
        ) : null}

        {plan && totals ? (
          <section aria-labelledby="plan-summary-heading">
            <h3 id="plan-summary-heading">Plan summary</h3>
            <p>
              add {totals.create} · change {totals.update} · replace{" "}
              {totals.replace} · destroy {totals.delete}
            </p>
            {plan.units.map((unit) => (
              <div key={unit.dir} className="plan-unit">
                <h3 className="plan-unit-dir">{unit.dir}</h3>
                <p className="table-detail">
                  add {unit.create} · change {unit.update} · replace{" "}
                  {unit.replace} · destroy {unit.delete}
                </p>
                <ul className="plan-unit-changes">
                  {unit.changes.map((change, index) => (
                    <li key={`${change.address}-${index}`} className="change-item">
                      <span
                        className={
                          change.action === "replace" || change.action === "delete"
                            ? "change-destructive"
                            : "change-action"
                        }
                      >
                        {change.action}
                      </span>{" "}
                      <span className="change-address">{change.address}</span>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </section>
        ) : null}

        <h3>Files</h3>
        {detail.diff.length === 0 ? (
          <p className="empty-state">No file changes in this draft.</p>
        ) : (
          detail.diff.map((file) => (
            <div key={file.path} className="diff-file">
              <p className="diff-file-header">
                <strong>{file.path}</strong> <span className="table-detail">{file.status}</span>
              </p>
              <pre className="diff-patch">{file.patch || "Binary or empty change."}</pre>
            </div>
          ))
        )}
      </section>
    );
  }

  return (
    <section aria-labelledby="changesets-heading">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Changes</p>
          <h2 id="changesets-heading">Changesets</h2>
        </div>
        {onBack ? (
          <Button variant="secondary" onClick={onBack}>
            Back
          </Button>
        ) : null}
      </div>

      {loadError ? <Banner variant="error">{loadError}</Banner> : null}

      {changesets === null && !loadError ? (
        <p className="status" role="status">
          Loading changesets…
        </p>
      ) : null}

      {changesets !== null && changesets.length === 0 ? (
        <div className="empty-state">
          <p>No changesets yet. Run a command to start a draft.</p>
        </div>
      ) : null}

      {changesets !== null && changesets.length > 0 ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Changeset</th>
                <th>Status</th>
                <th>Revision</th>
              </tr>
            </thead>
            <tbody>
              {changesets.map((item) => (
                <tr key={item.id}>
                  <td>
                    <button
                      type="button"
                      className="btn-secondary action-btn"
                      onClick={() => {
                        setProblem(null);
                        setOperation(null);
                        setSelectedId(item.id);
                      }}
                    >
                      {shortId(item.id)}
                    </button>
                  </td>
                  <td>
                    <StatusBadge powerState={item.status} />
                  </td>
                  <td>{item.revision}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </section>
  );
}
