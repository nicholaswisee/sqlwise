import { createFileRoute, Link } from "@tanstack/react-router";
import { Brand } from "../components/workspace";
import { styles } from "../components/styles";

export const Route = createFileRoute("/")({ component: Home });

function Home() {
  const loginPath: string = "/login";
  return (
    <div className="flex min-h-screen flex-col bg-slate-100 text-slate-900">
      <header className="border-b border-slate-200 bg-white">
        <div className="mx-auto flex w-full max-w-5xl flex-wrap items-center justify-between gap-x-6 gap-y-1 px-4 py-1.5 sm:px-6">
          <Brand href="/" />
          <Link to={loginPath} className={styles.primary}>
            Sign in
          </Link>
        </div>
      </header>
      <main className="mx-auto flex w-full max-w-5xl flex-1 items-center justify-center px-4 py-16 sm:px-6 sm:py-24">
        <div className="flex max-w-2xl flex-col items-center gap-5 text-center">
          <p className="text-sm font-medium text-slate-600">PostgreSQL workspace</p>
          <h1 className="text-5xl font-semibold tracking-tight text-slate-900 sm:text-6xl">
            sqlwise
          </h1>
          <p className="text-xl leading-8 text-slate-800">PostgreSQL Management Platform</p>
          <p className="max-w-xl text-base leading-7 text-slate-600">
            A starting point for managed access, database exploration, and controlled schema
            changes.
          </p>
          <Link to={loginPath} className={`${styles.primary} mt-2`}>
            Sign in
          </Link>
        </div>
      </main>
      <footer className="border-t border-slate-200 bg-white">
        <p className="mx-auto w-full max-w-5xl px-4 py-5 text-sm text-slate-600 sm:px-6">
          IF3110 Web Application Development
        </p>
      </footer>
    </div>
  );
}
