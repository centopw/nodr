import { useEffect, useState, type FormEvent } from "react";
import {
  connectRepository,
  listInstallations,
  ProblemError,
  type Installation,
  type LedgerWorkspace,
  type ProblemDetails,
} from "./api";
import Banner from "./components/Banner";
import Button from "./components/Button";
import FieldErrorList from "./components/FieldErrorList";

export interface RepositoriesPanelProps {
  onConnected: (workspace: LedgerWorkspace) => void | Promise<void>;
  collapsible?: boolean;
}

interface RepositoryOption {
  key: string;
  installationId: number;
  account: string;
  owner: string;
  name: string;
  defaultBranch: string;
  private: boolean;
}

export default function RepositoriesPanel({
  onConnected,
  collapsible = false,
}: RepositoriesPanelProps) {
  const [expanded, setExpanded] = useState(!collapsible);
  const [installations, setInstallations] = useState<Installation[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [choice, setChoice] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [problem, setProblem] = useState<ProblemDetails | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  useEffect(() => {
    if (!expanded) {
      return;
    }
    let cancelled = false;
    setLoadError(null);
    listInstallations()
      .then((items) => {
        if (!cancelled) {
          setInstallations(items);
        }
      })
      .catch((error: unknown) => {
        if (!cancelled) {
          setLoadError(
            error instanceof ProblemError && (error.problem.status === 404 || error.problem.status === 405)
              ? "GitHub is not configured on this server. Set the NODR_GITHUB_* environment variables and restart nodr."
              : error instanceof Error
                ? error.message
                : "Unable to load GitHub installations.",
          );
        }
      });
    return () => {
      cancelled = true;
    };
  }, [expanded]);

  const options: RepositoryOption[] = (installations ?? []).flatMap(
    (installation) =>
      installation.repositories.map((repo) => ({
        key: `${installation.id}:${repo.owner}/${repo.name}`,
        installationId: installation.id,
        account: installation.account,
        owner: repo.owner,
        name: repo.name,
        defaultBranch: repo.defaultBranch,
        private: repo.private,
      })),
  );
  const selected = options.find((option) => option.key === choice);

  async function handleConnect(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected) {
      return;
    }
    setSubmitting(true);
    setProblem(null);
    setSuccess(null);
    try {
      const workspace = await connectRepository({
        installationId: selected.installationId,
        owner: selected.owner,
        repository: selected.name,
      });
      setChoice("");
      setSuccess(
        `Connected ${workspace.owner}/${workspace.repository} as workspace ${workspace.name}.`,
      );
      await onConnected(workspace);
    } catch (error) {
      setProblem(
        error instanceof ProblemError
          ? error.problem
          : {
              type: "about:blank",
              title: "Error",
              status: 500,
              detail:
                error instanceof Error ? error.message : String(error),
            },
      );
    } finally {
      setSubmitting(false);
    }
  }

  const problemErrors = problem?.errors ?? [];
  const repositoryErrors = problemErrors.filter(
    (error) => error.path === "repository",
  );
  const otherErrors = problemErrors.filter(
    (error) => error.path !== "repository",
  );

  return (
    <section aria-labelledby="repositories-heading">
      <div className="section-heading">
        <div>
          <p className="eyebrow">GitHub</p>
          <h2 id="repositories-heading">Connect GitHub repository</h2>
        </div>
        {collapsible ? (
          <Button
            variant="secondary"
            size="small"
            aria-expanded={expanded}
            onClick={() => setExpanded((current) => !current)}
          >
            {expanded ? "Collapse" : "Connect repository"}
          </Button>
        ) : null}
      </div>

      {expanded ? (
        <>
          {success ? <Banner variant="success">{success}</Banner> : null}
          {loadError ? <Banner variant="error">{loadError}</Banner> : null}
          {problem ? (
            <Banner variant="error">
              <p>
                <strong>{problem.title || "Error"}:</strong> {problem.detail}
              </p>
              <FieldErrorList errors={otherErrors} />
            </Banner>
          ) : null}

          {installations === null && !loadError ? (
            <p className="status" role="status">
              Loading GitHub installations…
            </p>
          ) : null}

          {installations !== null && options.length === 0 ? (
            <div className="empty-state">
              <p>
                No repositories are available to the nodr GitHub App. Install
                the app on a repository, then reload this page.
              </p>
            </div>
          ) : null}

          {options.length > 0 ? (
            <form onSubmit={handleConnect} autoComplete="off">
              <div className="form-grid">
                <div className="field">
                  <label htmlFor="repository-choice">Repository</label>
                  <select
                    id="repository-choice"
                    value={choice}
                    onChange={(event) => setChoice(event.target.value)}
                    required
                    aria-invalid={repositoryErrors.length > 0}
                    aria-describedby={
                      repositoryErrors.length > 0 ? "repository-errors" : undefined
                    }
                  >
                    <option value="" disabled>
                      Choose a repository
                    </option>
                    {options.map((option) => (
                      <option key={option.key} value={option.key}>
                        {option.owner}/{option.name}
                        {option.private ? " (private)" : ""} — {option.account}
                      </option>
                    ))}
                  </select>
                  <FieldErrorList
                    errors={repositoryErrors}
                    id="repository-errors"
                  />
                </div>
                {selected ? (
                  <div className="field">
                    <span>Default branch</span>
                    <code>{selected.defaultBranch}</code>
                  </div>
                ) : null}
              </div>
              <div className="form-actions">
                <Button type="submit" disabled={submitting || !selected}>
                  {submitting ? "Connecting…" : "Connect repository"}
                </Button>
              </div>
            </form>
          ) : null}
        </>
      ) : null}
    </section>
  );
}
