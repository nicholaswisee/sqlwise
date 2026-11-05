import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useAuthenticatedContext } from "../../lib/authenticated-context";
import { ApiError } from "../../lib/api";
import { WorkspaceTopbar } from "../../components/workspace";
import { styles } from "../../components/styles";

export const Route = createFileRoute("/_authenticated/account")({
  head: () => ({ meta: [{ title: "Your account | sqlwise" }] }),
  component: Account,
});

function Account() {
  const { auth, session } = useAuthenticatedContext();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  async function refresh() {
    setBusy(true);
    setMessage("");
    try {
      await auth.session();
    } catch (error) {
      setMessage(
        error instanceof ApiError
          ? error.message
          : "Unable to refresh your session. Check your connection and try again.",
      );
    } finally {
      setBusy(false);
    }
  }
  async function logout() {
    setBusy(true);
    // clear() in finally unmounts this subtree; the login page reports failure.
    try {
      await auth.logout();
    } catch {
      /* Server signout is unconfirmed. */
    }
  }

  const adminPath: string = "/admin";
  return (
    <div className="flex min-h-screen flex-col bg-slate-100 text-slate-900">
      <WorkspaceTopbar username={session.user.username}>
        <nav
          aria-label="Breadcrumb"
          className="flex min-w-0 flex-wrap items-center gap-2 text-sm text-slate-600"
        >
          <span aria-current="page" className="font-medium text-slate-900">
            Account
          </span>
        </nav>
      </WorkspaceTopbar>
      <main className="mx-auto w-full max-w-7xl flex-1 px-4 py-8 sm:px-6">
        <h1 className={styles.heading}>Your account</h1>
        <div className="mt-6 flex flex-wrap items-start gap-6">
          <section
            aria-labelledby="account-targets"
            className={`${styles.panel} min-w-0 flex-[999_1_36rem] p-5`}
          >
            <h2 id="account-targets" className="text-lg font-semibold text-slate-900">
              Your database access
            </h2>
            {session.needs_refresh ? (
              <p
                role="status"
                className="mt-3 rounded-lg border border-slate-200 bg-slate-50 p-4 text-sm leading-6 text-slate-800"
              >
                Your permissions changed. Refresh this page to see your current access.
              </p>
            ) : (
              session.databases.length === 0 && (
                <p className="mt-3 rounded-lg border border-dashed border-slate-300 bg-white p-6 text-sm leading-6 text-slate-600">
                  No database access has been assigned to your account.
                </p>
              )
            )}
            <ul className="mt-2 divide-y divide-slate-100 empty:hidden">
              {session.databases.map((database) => {
                const href: string = `/databases/${encodeURIComponent(database.id)}`;
                return (
                  <li
                    key={database.id}
                    className="flex flex-wrap items-center justify-between gap-3 py-1.5"
                  >
                    <Link
                      to={href}
                      className={`${styles.link} inline-flex min-h-11 items-center break-all text-sm font-medium`}
                    >
                      {database.name}
                    </Link>
                    <span
                      className={
                        database.permission.toLowerCase() === "edit" ? styles.chipTeal : styles.chip
                      }
                    >
                      {database.permission}
                    </span>
                  </li>
                );
              })}
            </ul>
          </section>
          <aside aria-label="Account" className={`${styles.panel} min-w-0 flex-[1_1_18rem] p-5`}>
            <p className="break-words text-sm leading-6 text-slate-800">
              Signed in as{" "}
              <strong className="font-semibold text-slate-900">{session.user.username}</strong>.
            </p>
            <div
              id="session-status"
              role="status"
              aria-live="polite"
              className="mt-6 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-slate-200 bg-slate-50 py-2 pr-2 pl-4 text-sm text-slate-800"
            >
              <span className="inline-flex items-center gap-2">
                <span aria-hidden="true" className="size-2 rounded-full bg-emerald-600" />
                {busy ? "Checking session…" : "Session active"}
              </span>
              <button
                type="button"
                disabled={busy}
                onClick={() => void refresh()}
                className={styles.button}
              >
                Refresh session
              </button>
            </div>
            {message && (
              <p role="alert" className={`${styles.alert} mt-4`}>
                {message}
              </p>
            )}
            {session.user.is_platform_admin && (
              <Link to={adminPath} className={`${styles.button} mt-4 w-full`}>
                Manage platform
              </Link>
            )}
            <form
              className="mt-6"
              onSubmit={(event) => {
                event.preventDefault();
                void logout();
              }}
            >
              <button type="submit" disabled={busy} className={`${styles.button} w-full`}>
                Sign out
              </button>
            </form>
          </aside>
        </div>
      </main>
    </div>
  );
}
