import { useState, type FormEvent } from "react";
import { ProblemError, login, type LoginParams } from "./api";
import Banner from "./components/Banner";
import Button from "./components/Button";
import FieldErrorList from "./components/FieldErrorList";
import type { FieldError } from "./api";

interface LoginFormProps {
  onLoggedIn: () => void;
}

export default function LoginForm({ onLoggedIn }: LoginFormProps) {
  const [values, setValues] = useState<LoginParams>({
    username: "",
    password: "",
  });
  const [problem, setProblem] = useState<ProblemError["problem"] | null>(null);
  const [unexpectedError, setUnexpectedError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const errorsFor = (field: keyof LoginParams): FieldError[] =>
    problem?.errors?.filter((error) =>
      error.path.startsWith(`params.${field}`),
    ) ?? [];

  function updateField<K extends keyof LoginParams>(
    field: K,
    value: LoginParams[K],
  ) {
    setValues((current) => ({ ...current, [field]: value }));
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setProblem(null);
    setUnexpectedError(null);
    setSubmitting(true);
    try {
      await login(values);
      onLoggedIn();
    } catch (error) {
      if (error instanceof ProblemError) {
        setProblem(error.problem);
      } else {
        setUnexpectedError(
          error instanceof Error ? error.message : "Unable to sign in.",
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
      <section aria-labelledby="login-heading">
        <div className="section-heading">
          <div>
            <p className="eyebrow">nodr</p>
            <h1 id="login-heading">Sign in</h1>
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
              <label htmlFor="login-username">Username</label>
              <input
                id="login-username"
                name="username"
                type="text"
                autoComplete="username"
                value={values.username}
                onChange={(event) => updateField("username", event.target.value)}
                aria-describedby={
                  errorsFor("username").length ? "login-username-errors" : undefined
                }
                aria-invalid={errorsFor("username").length > 0}
                required
              />
              <FieldErrorList errors={errorsFor("username")} id="login-username-errors" />
            </div>
            <div className="field">
              <label htmlFor="login-password">Password</label>
              <input
                id="login-password"
                name="password"
                type="password"
                autoComplete="current-password"
                value={values.password}
                onChange={(event) => updateField("password", event.target.value)}
                aria-describedby={
                  errorsFor("password").length ? "login-password-errors" : undefined
                }
                aria-invalid={errorsFor("password").length > 0}
                required
              />
              <FieldErrorList errors={errorsFor("password")} id="login-password-errors" />
            </div>
          </div>
          <div className="form-actions">
            <Button type="submit" disabled={submitting}>
              {submitting ? "Signing in…" : "Sign in"}
            </Button>
          </div>
        </form>
      </section>
    </main>
  );
}