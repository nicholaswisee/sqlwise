import { useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { Navigate } from "@tanstack/react-router";
import { AuthenticatedContext } from "../../lib/authenticated-context";
import type { AppContext } from "../../lib/query-client";
import { styles } from "../../components/styles";

export function AuthGate({ context, children }: { context: AppContext; children: ReactNode }) {
  const { auth } = context;
  const snapshot = useSyncExternalStore(auth.subscribe, auth.getSnapshot, auth.getSnapshot);
  const [attempt, setAttempt] = useState(0);
  const [status, setStatus] = useState<"pending" | "ready" | "error">("pending");

  useEffect(() => {
    if (!auth.getSnapshot().session && auth.getSnapshot().revision > 0) return;
    const controller = new AbortController();
    setStatus("pending");
    void auth.session({ signal: controller.signal }).then(
      () => {
        if (!controller.signal.aborted) setStatus("ready");
      },
      () => {
        if (!controller.signal.aborted) setStatus("error");
      },
    );
    return () => controller.abort();
  }, [auth, attempt]);

  if (!snapshot.session && (snapshot.revision > 0 || status === "ready"))
    return <Navigate to="/login" replace />;
  if (status === "error")
    return (
      <main className="mx-auto max-w-5xl px-4 py-16">
        <p role="alert" className={styles.alert}>
          Unable to check your session. Check your connection and try again.
        </p>
        <button
          type="button"
          className={`${styles.button} mt-4`}
          onClick={() => setAttempt((value) => value + 1)}
        >
          Retry
        </button>
      </main>
    );
  if (status !== "ready" || !snapshot.session)
    return (
      <main className="mx-auto max-w-5xl px-4 py-16">
        <p role="status">Checking your session…</p>
      </main>
    );
  return (
    <AuthenticatedContext key={snapshot.revision} value={{ ...context, session: snapshot.session }}>
      {children}
    </AuthenticatedContext>
  );
}
