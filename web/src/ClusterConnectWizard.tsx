import { useState, type FormEvent } from "react";
import {
  clusterConnect,
  clusterConnectProbe,
  ProblemError,
  type ClusterConnectParams,
  type ProblemDetails,
} from "./api";
import Banner from "./components/Banner";
import Button from "./components/Button";

interface ClusterConnectWizardProps {
  workspace: string;
  onConnected: (summary: string) => void | Promise<void>;
}

type Step = "details" | "confirm";

const initialValues: ClusterConnectParams = {
  cluster: "",
  endpoint: "",
  node: "",
  adminUsername: "root",
  adminPassword: "",
};

export default function ClusterConnectWizard({
  workspace,
  onConnected,
}: ClusterConnectWizardProps) {
  const [values, setValues] = useState<ClusterConnectParams>(initialValues);
  const [step, setStep] = useState<Step>("details");
  const [fingerprint, setFingerprint] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [problem, setProblem] = useState<ProblemDetails | null>(null);

  function update(field: keyof ClusterConnectParams, value: string) {
    setValues((current) => ({ ...current, [field]: value }));
  }

  async function handleProbe(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setProblem(null);
    try {
      const result = await clusterConnectProbe(workspace, values);
      setFingerprint(result.fingerprint);
      setStep("confirm");
    } catch (error) {
      setProblem(error instanceof ProblemError ? error.problem : { type: "about:blank", title: "Error", status: 500, detail: error instanceof Error ? error.message : String(error) });
    } finally {
      setSubmitting(false);
    }
  }

  async function handleConfirm() {
    setSubmitting(true);
    setProblem(null);
    try {
      await clusterConnect(workspace, { ...values, fingerprint });
      setValues(initialValues);
      setFingerprint("");
      setStep("details");
      await onConnected(`Connected cluster ${values.cluster}.`);
    } catch (error) {
      setProblem(error instanceof ProblemError ? error.problem : { type: "about:blank", title: "Error", status: 500, detail: error instanceof Error ? error.message : String(error) });
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <section aria-labelledby="connect-cluster-heading">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Proxmox VE</p>
          <h2 id="connect-cluster-heading">Connect cluster</h2>
        </div>
      </div>
      {problem ? <Banner variant="error">{problem.detail}</Banner> : null}
      {step === "details" ? (
        <form onSubmit={handleProbe} autoComplete="off">
          <div className="form-grid">
            <div className="field"><label htmlFor="cluster-name">Cluster name</label><input id="cluster-name" value={values.cluster} onChange={(event) => update("cluster", event.target.value)} required /></div>
            <div className="field"><label htmlFor="cluster-endpoint">API URL</label><input id="cluster-endpoint" type="url" value={values.endpoint} onChange={(event) => update("endpoint", event.target.value)} placeholder="https://pve.example:8006" required /></div>
            <div className="field"><label htmlFor="cluster-node">Node</label><input id="cluster-node" value={values.node} onChange={(event) => update("node", event.target.value)} required /></div>
            <div className="field"><label htmlFor="cluster-admin-user">Administrator username</label><input id="cluster-admin-user" value={values.adminUsername} onChange={(event) => update("adminUsername", event.target.value)} required /></div>
            <div className="field"><label htmlFor="cluster-admin-password">Administrator password</label><input id="cluster-admin-password" type="password" value={values.adminPassword} onChange={(event) => update("adminPassword", event.target.value)} required /></div>
          </div>
          <div className="form-actions"><Button type="submit" disabled={submitting}>{submitting ? "Checking certificate…" : "Check certificate"}</Button></div>
        </form>
      ) : (
        <div className="message message-warning">
          <p>Verify this certificate fingerprint with the Proxmox VE administrator before trusting it:</p>
          <p><code>{fingerprint}</code></p>
          <div className="form-actions">
            <Button variant="secondary" onClick={() => { setFingerprint(""); setStep("details"); }}>Back</Button>
            <Button onClick={() => void handleConfirm()} disabled={submitting}>{submitting ? "Connecting…" : "Trust and connect"}</Button>
          </div>
        </div>
      )}
    </section>
  );
}
