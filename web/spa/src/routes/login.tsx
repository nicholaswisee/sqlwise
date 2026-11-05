import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { ApiError } from "../lib/api";
import { AuthShell } from "../features/auth/auth-shell";
import { styles } from "../components/styles";

export const Route = createFileRoute("/login")({
  head: () => ({ meta: [{ title: "Sign in | sqlwise" }] }),
  component: Login,
});

function Login() {
  const { auth } = Route.useRouteContext();
  const snapshot = useSyncExternalStore(auth.subscribe, auth.getSnapshot, auth.getSnapshot);
  const navigate = useNavigate();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [message, setMessage] = useState("");
  const feedback =
    message ||
    (snapshot.logoutUnconfirmed
      ? "You are signed out locally, but server signout is unconfirmed. Sign in explicitly to continue."
      : "");
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [retry, setRetry] = useState(0);
  const [challengeFailed, setChallengeFailed] = useState(false);
  const active = useRef<AbortController | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    active.current = controller;
    setReady(false);
    setChallengeFailed(false);
    void (async () => {
      try {
        // A local clear is authoritative even if a failed logout left a cookie.
        if (auth.getSnapshot().revision === 0) {
          const session = await auth.session({ signal: controller.signal });
          if (controller.signal.aborted) return;
          if (session) {
            await navigate({ to: "/account", replace: true });
            return;
          }
        }
        await auth.loginChallenge({ signal: controller.signal });
        if (!controller.signal.aborted) setReady(true);
      } catch (error) {
        if (controller.signal.aborted) return;
        setMessage(
          error instanceof ApiError
            ? error.message
            : "Unable to prepare sign in. Check your connection and try again.",
        );
        setChallengeFailed(true);
      }
    })();
    return () => {
      controller.abort();
      active.current = null;
    };
  }, [auth, navigate, retry]);

  async function submit(event: React.SubmitEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!ready || busy) return;
    const controller = active.current;
    if (!controller || controller.signal.aborted) return;
    setBusy(true);
    setMessage("");
    try {
      await auth.login({ username, password }, { signal: controller.signal });
      setPassword("");
      const session = await auth.session({ signal: controller.signal });
      if (!session) throw new Error("Session could not be confirmed.");
      if (!controller.signal.aborted) await navigate({ to: "/account", replace: true });
    } catch (error) {
      if (controller.signal.aborted) return;
      setPassword("");
      setMessage(
        error instanceof ApiError
          ? error.message
          : "Unable to sign in. Check your connection and try again.",
      );
      if (
        !(error instanceof ApiError) ||
        error.code === "csrf_invalid" ||
        (error.status === 401 && error.code !== "credentials_invalid")
      ) {
        setReady(false);
        try {
          await auth.loginChallenge({ signal: controller.signal });
          if (!controller.signal.aborted) setReady(true);
        } catch {
          if (!controller.signal.aborted) setChallengeFailed(true);
        }
      }
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }

  return (
    <AuthShell>
      <p className={`${styles.muted} mt-2 leading-6`}>
        Use the account provided by your platform administrator.
      </p>
      <p id="login-error" role="alert" hidden={!feedback} className={`${styles.alert} mt-6`}>
        {feedback}
      </p>
      {challengeFailed && (
        <button
          type="button"
          className={`${styles.button} mt-4`}
          onClick={() => {
            setMessage("");
            setRetry((value) => value + 1);
          }}
        >
          Retry
        </button>
      )}
      <form onSubmit={submit} className="mt-6 space-y-5">
        <div className="space-y-1.5">
          <label htmlFor="username" className={styles.label}>
            Username
          </label>
          <input
            id="username"
            name="username"
            type="text"
            value={username}
            onChange={(event) => setUsername(event.target.value)}
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            required
            maxLength={64}
            aria-describedby="login-error"
            className={styles.input}
          />
        </div>
        <div className="space-y-1.5">
          <label htmlFor="password" className={styles.label}>
            Password
          </label>
          <input
            id="password"
            name="password"
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            autoComplete="current-password"
            required
            maxLength={128}
            aria-describedby="login-error"
            className={styles.input}
          />
        </div>
        <button type="submit" disabled={!ready || busy} className={`${styles.primary} w-full`}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </AuthShell>
  );
}
