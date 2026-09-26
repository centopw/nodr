import { useState, type FormEvent } from "react";
import {
  ProblemError,
  createVM,
  type CreateVMParams,
  type FieldError,
  type NamedResource,
  type VMSize,
} from "./api";
import Banner from "./components/Banner";
import Button from "./components/Button";
import FieldErrorList from "./components/FieldErrorList";

interface NewVMFormProps {
  workspace: string;
  environments: string[];
  clusters: NamedResource[];
  templates: NamedResource[];
  networks: NamedResource[];
  onCreated: (summary: string) => Promise<void>;
  onCancel: () => void;
}

const sizes: Array<{
  value: VMSize;
  label: string;
}> = [
  { value: "S", label: "S — 1 vCPU, 2 GiB RAM, 20 GiB disk" },
  { value: "M", label: "M — 2 vCPUs, 4 GiB RAM, 40 GiB disk" },
  { value: "L", label: "L — 4 vCPUs, 8 GiB RAM, 80 GiB disk" },
];

export default function NewVMForm({
  workspace,
  environments,
  clusters,
  templates,
  networks,
  onCreated,
  onCancel,
}: NewVMFormProps) {
  const [values, setValues] = useState<CreateVMParams>({
    name: "",
    environment: environments[0] ?? "",
    cluster: clusters[0]?.name ?? "",
    template: templates[0]?.name ?? "",
    network: networks[0]?.name ?? "",
    storage: "",
    size: "M",
  });
  const [problem, setProblem] = useState<ProblemError["problem"] | null>(null);
  const [unexpectedError, setUnexpectedError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const errorsFor = (field: keyof CreateVMParams): FieldError[] =>
    problem?.errors?.filter((error) =>
      error.path.startsWith(`params.${field}`),
    ) ?? [];

  function updateField<K extends keyof CreateVMParams>(
    field: K,
    value: CreateVMParams[K],
  ) {
    setValues((current) => ({ ...current, [field]: value }));
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setProblem(null);
    setUnexpectedError(null);
    setSubmitting(true);

    try {
      const result = await createVM(workspace, values);
      await onCreated(result.summary);
    } catch (error) {
      if (error instanceof ProblemError) {
        setProblem(error.problem);
      } else {
        setUnexpectedError(
          error instanceof Error ? error.message : "Unable to create the VM.",
        );
      }
    } finally {
      setSubmitting(false);
    }
  }

  function renderFieldErrors(field: keyof CreateVMParams) {
    return (
      <FieldErrorList errors={errorsFor(field)} id={`${field}-errors`} />
    );
  }

  const errorSummary = problem ??
    (unexpectedError
      ? {
          detail: unexpectedError,
          errors: undefined,
        }
      : null);

  return (
    <section aria-labelledby="new-vm-heading">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Virtual machines</p>
          <h2 id="new-vm-heading">New VM</h2>
        </div>
      </div>

      <form onSubmit={handleSubmit} autoComplete="off">
        {errorSummary ? (
          <Banner variant="error">
            <p>{errorSummary.detail}</p>
            {errorSummary.errors && errorSummary.errors.length > 0 ? (
              <ul>
                {errorSummary.errors.map((error, index) => (
                  <li key={`${error.path}-${index}`}>
                    <strong>{error.path}:</strong> {error.message}
                  </li>
                ))}
              </ul>
            ) : null}
          </Banner>
        ) : null}

        <div className="form-grid">
          <div className="field">
            <label htmlFor="name">Name</label>
            <input
              id="name"
              name="name"
              type="text"
              value={values.name}
              onChange={(event) => updateField("name", event.target.value)}
              aria-describedby={errorsFor("name").length ? "name-errors" : undefined}
              aria-invalid={errorsFor("name").length > 0}
              required
            />
            {renderFieldErrors("name")}
          </div>

          <div className="field">
            <label htmlFor="environment">Environment</label>
            <select
              id="environment"
              name="environment"
              value={values.environment}
              onChange={(event) => updateField("environment", event.target.value)}
              aria-describedby={
                errorsFor("environment").length
                  ? "environment-errors"
                  : undefined
              }
              aria-invalid={errorsFor("environment").length > 0}
              required
            >
              {environments.map((environment) => (
                <option key={environment} value={environment}>
                  {environment}
                </option>
              ))}
            </select>
            {renderFieldErrors("environment")}
          </div>

          <div className="field">
            <label htmlFor="cluster">Cluster</label>
            <select
              id="cluster"
              name="cluster"
              value={values.cluster}
              onChange={(event) => updateField("cluster", event.target.value)}
              aria-describedby={
                errorsFor("cluster").length ? "cluster-errors" : undefined
              }
              aria-invalid={errorsFor("cluster").length > 0}
              required
            >
              {clusters.map((cluster) => (
                <option key={cluster.name} value={cluster.name}>
                  {cluster.name}
                </option>
              ))}
            </select>
            {renderFieldErrors("cluster")}
          </div>

          <div className="field">
            <label htmlFor="template">Template</label>
            <select
              id="template"
              name="template"
              value={values.template}
              onChange={(event) => updateField("template", event.target.value)}
              aria-describedby={
                errorsFor("template").length ? "template-errors" : undefined
              }
              aria-invalid={errorsFor("template").length > 0}
              required
            >
              {templates.map((template) => (
                <option key={template.name} value={template.name}>
                  {template.name}
                </option>
              ))}
            </select>
            {renderFieldErrors("template")}
          </div>

          <div className="field">
            <label htmlFor="network">Network</label>
            <select
              id="network"
              name="network"
              value={values.network}
              onChange={(event) => updateField("network", event.target.value)}
              aria-describedby={
                errorsFor("network").length ? "network-errors" : undefined
              }
              aria-invalid={errorsFor("network").length > 0}
              required
            >
              {networks.map((network) => (
                <option key={network.name} value={network.name}>
                  {network.name}
                </option>
              ))}
            </select>
            {renderFieldErrors("network")}
          </div>

          <div className="field">
            <label htmlFor="storage">Storage</label>
            <input
              id="storage"
              name="storage"
              type="text"
              value={values.storage}
              onChange={(event) => updateField("storage", event.target.value)}
              aria-describedby={
                errorsFor("storage").length ? "storage-errors" : undefined
              }
              aria-invalid={errorsFor("storage").length > 0}
              required
            />
            {renderFieldErrors("storage")}
          </div>
        </div>

        <fieldset>
          <legend>Size</legend>
          <div className="size-options">
            {sizes.map((size) => (
              <label className="radio-option" key={size.value}>
                <input
                  type="radio"
                  name="size"
                  value={size.value}
                  checked={values.size === size.value}
                  onChange={() => updateField("size", size.value)}
                  aria-describedby={
                    errorsFor("size").length ? "size-errors" : undefined
                  }
                />
                <span>{size.label}</span>
              </label>
            ))}
          </div>
          {renderFieldErrors("size")}
        </fieldset>

        <div className="form-actions">
          <Button variant="secondary" onClick={onCancel}>
            Cancel
          </Button>
          <Button type="submit" disabled={submitting}>
            {submitting ? "Creating…" : "Create VM"}
          </Button>
        </div>
      </form>
    </section>
  );
}
