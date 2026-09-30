import { useState, type FormEvent } from "react";
import { ProblemError, setup, type SetupParams } from "./api";
import Banner from "./components/Banner";
import Button from "./components/Button";
import FieldErrorList from "./components/FieldErrorList";
import type { FieldError } from "./api";

interface SetupFormProps {
  onSetupComplete: () => void;
}

export default function SetupForm({ onSetupComplete }: SetupFormProps) {
  const [values, setValues] = useState<SetupParams>({
    token: "",
    username: "",
    password: "",
  });
  const [problem, setProblem] = useState<ProblemError["problem"] | null>(null);
  const [unexpectedError, setUnexpectedError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const errorsFor = (field: keyof SetupParams): FieldError[] =>
    problem?.errors?.filter((error) =>
      error.path.startsWith(`params.${field}`),
    ) ?? [];

  function updateField<K extends keyof SetupParams>(
    field: K,
    value: SetupParams[K],
  ) {
    setValues((current) => ({ ...current, [field]: value }));
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setProblem(null);
    setUnexpectedError(null);
    setSubmitting(true);
    try {
      await setup(values);
      onSetupComplete();
    } catch (error) {
      if (error instanceof ProblemError) {
        setProblem(error.problem);
      } else {
        setUnexpectedError(
          error instanceof Error ? error.message : "Unable to complete setup.",
        );
      }
    } finally {
      setSubmitting(false);
    }
  }

  const errorSummary = problem ??
    (unexpectedError ? { detail: unexpectedError, errors: undefined } : null);

  return (
    <main className="app-shell-centered">
      <section aria-labelledby="setup-heading">
        <div className="section-heading">
          <div>
            <p className="eyebrow">nodr</p>
            <h1 id="setup-heading">Set up your administrator account</h1>
          </div>
        </div>
        <form onSubmit={handleSubmit} autoComplete="off">
          {errorSummary ? (
            <Banner variant="error">
              <p>{errorSummary.detail}</p>
            </Banner>
          ) : null}
          <div className="form-grid">
            <div className="field">
              <label htmlFor="setup-token">Bootstrap token</label>
              <input
                id="setup-token"
                name="token"
                type="password"
                value={values.token}
                onChange={(event) => updateField("token", event.target.value)}
                aria-describedby={
                  errorsFor("token").length ? "setup-token-errors" : undefined
                }
                aria-invalid={errorsFor("token").length > 0}
                required
              />
              <FieldErrorList errors={errorsFor("token")} id="setup-token-errors" />
            </div>
            <div className="field">
              <label htmlFor="setup-username">Username</label>
              <input
                id="setup-username"
                name="username"
                type="text"
                autoComplete="username"
                value={values.username}
                onChange={(event) => updateField("username", event.target.value)}
                aria-describedby={
                  errorsFor("username").length ? "setup-username-errors" : undefined
                }
                aria-invalid={errorsFor("username").length > 0}
                required
              />
              <FieldErrorList errors={errorsFor("username")} id="setup-username-errors" />
            </div>
            <div className="field">
              <label htmlFor="setup-password">Password</label>
              <input
                id="setup-password"
                name="password"
                type="password"
                autoComplete="new-password"
                value={values.password}
                onChange={(event) => updateField("password", event.target.value)}
                aria-describedby={
                  errorsFor("password").length ? "setup-password-errors" : undefined
                }
                aria-invalid={errorsFor("password").length > 0}
                required
              />
              <FieldErrorList errors={errorsFor("password")} id="setup-password-errors" />
            </div>
          </div>
          <div className="form-actions">
            <Button type="submit" disabled={submitting}>
              {submitting ? "Setting up…" : "Create administrator account"}
            </Button>
          </div>
        </form>
      </section>
    </main>
  );
}