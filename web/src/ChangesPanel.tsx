import { useState } from "react";
import {
  applyChanges,
  planChanges,
  ProblemError,
  type ApplyResult,
  type ChangeAction,
  type PlanResult,
  type ProblemDetails,
} from "./api";

export interface ChangesPanelProps {
  workspace: string;
  onApplied?: () => void;
  onBack?: () => void;
}

const ACTION_LABELS: Record<ChangeAction, string> = {
  create: "add",
  update: "change",
  replace: "replace",
  delete: "destroy",
  read: "read",
  forget: "forget",
};

export default function ChangesPanel({
  workspace,
  onApplied,
  onBack,
}: ChangesPanelProps) {
  const [plan, setPlan] = useState<PlanResult | null>(null);
  const [planning, setPlanning] = useState(false);
  const [applying, setApplying] = useState(false);
  const [allowDestroy, setAllowDestroy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetails | null>(null);
  const [applyResult, setApplyResult] = useState<ApplyResult | null>(null);

  async function handlePlan() {
    setPlanning(true);
    setProblem(null);
    setApplyResult(null);
    setAllowDestroy(false);

    try {
      const result = await planChanges(workspace);
      setPlan(result);
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
              : "An unexpected error occurred while planning changes.",
        });
      }
    } finally {
      setPlanning(false);
    }
  }

  async function handleApply() {
    if (!plan) {
      return;
    }

    setApplying(true);
    setProblem(null);
    setApplyResult(null);

    try {
      const result = await applyChanges(workspace, plan.planId, allowDestroy);
      setApplyResult(result);
      setPlan(null);
      setAllowDestroy(false);
      onApplied?.();
    } catch (err: unknown) {
      if (err instanceof ProblemError) {
        if (err.problem.status === 404) {
          setPlan(null);
          setAllowDestroy(false);
        }
        setProblem(err.problem);
      } else {
        setProblem({
          type: "about:blank",
          title: "Error",
          status: 500,
          detail:
            err instanceof Error
              ? err.message
              : "An unexpected error occurred while applying changes.",
        });
      }
    } finally {
      setApplying(false);
    }
  }

  const applyDisabled =
    !plan || applying || (plan.hasDestructiveChanges && !allowDestroy);

  return (
    <section aria-labelledby="changes-heading">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Infrastructure</p>
          <h2 id="changes-heading">Changes</h2>
        </div>
        {onBack ? (
          <button type="button" className="button-secondary" onClick={onBack}>
            Back
          </button>
        ) : null}
      </div>

      <div className="changes-actions">
        <button
          type="button"
          onClick={handlePlan}
          disabled={planning || applying}
        >
          {planning ? "Planning…" : "Plan changes"}
        </button>
        <button
          type="button"
          onClick={handleApply}
          disabled={applyDisabled}
        >
          {applying ? "Applying…" : "Apply"}
        </button>
      </div>

      {problem ? (
        <div className="message message-error" role="alert">
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
          {problem.units && problem.units.length > 0 ? (
            <ul className="unit-outcomes">
              {problem.units.map((unit) => (
                <li key={unit.dir}>
                  <strong>{unit.dir}:</strong> {unit.outcome}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}

      {applyResult ? (
        <div className="message message-success" role="status">
          <p>Changes applied:</p>
          <ul className="unit-outcomes">
            {applyResult.units.map((unit) => (
              <li key={unit.dir}>
                <strong>{unit.dir}:</strong> {unit.outcome}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {plan && (plan.hasDestructiveChanges || (problem?.status === 400 && !allowDestroy)) ? (
        <div className="message message-warning" role="alert">
          <p>
            <strong>Warning:</strong> This plan contains destructive changes that
            will replace or destroy resources.
          </p>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={allowDestroy}
              onChange={(e) => setAllowDestroy(e.target.checked)}
            />
            <span>I understand this will replace or destroy resources</span>
          </label>
        </div>
      ) : null}

      {plan ? (
        !plan.hasChanges ? (
          <p className="empty-state">No changes.</p>
        ) : (
          <div className="plan-units">
            {plan.units.map((unit) => (
              <div key={unit.dir} className="plan-unit">
                <h3 className="plan-unit-dir">{unit.dir}</h3>
                {unit.changes.length === 0 ? (
                  <p className="plan-unit-empty">No changes in this unit.</p>
                ) : (
                  <ul className="plan-unit-changes">
                    {unit.changes.map((change, index) => {
                      const isDestructive =
                        change.action === "replace" || change.action === "delete";
                      const actionWord =
                        ACTION_LABELS[change.action] ?? change.action;
                      return (
                        <li
                          key={`${change.address}-${index}`}
                          className="change-item"
                        >
                          <span
                            className={
                              isDestructive
                                ? "change-destructive"
                                : "change-action"
                            }
                          >
                            {actionWord}
                          </span>{" "}
                          <span className="change-address">{change.address}</span>
                        </li>
                      );
                    })}
                  </ul>
                )}
              </div>
            ))}
          </div>
        )
      ) : null}
    </section>
  );
}
