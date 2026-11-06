import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { styles } from "../../components/styles";

export function AuthShell({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col bg-slate-100 text-slate-900 lg:flex-row">
      <aside
        aria-label="About sqlwise"
        className="border-b border-slate-200 bg-white px-4 py-1.5 sm:px-6 lg:flex lg:w-[44%] lg:max-w-2xl lg:flex-col lg:gap-12 lg:border-r lg:border-b-0 lg:px-14 lg:py-12"
      >
        <Link
          to="/"
          className={`inline-flex min-h-11 w-fit items-center gap-2 text-base font-bold tracking-tight text-cyan-900 lg:text-lg ${styles.focus}`}
        >
          <svg
            width="24"
            height="24"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.75"
            strokeLinecap="round"
            aria-hidden="true"
          >
            <ellipse cx="12" cy="6" rx="7" ry="3" />
            <path d="M5 6v6c0 1.7 3.1 3 7 3s7-1.3 7-3V6" />
            <path d="M5 12v6c0 1.7 3.1 3 7 3s7-1.3 7-3v-6" />
          </svg>
          sqlwise
        </Link>
        <div className="hidden max-w-lg space-y-5 lg:my-auto lg:block">
          <p className="text-4xl leading-tight font-semibold tracking-tight text-slate-900">
            Every schema change, in order, with a record of what ran.
          </p>
          <p className="text-lg leading-8 text-slate-600">
            Browse PostgreSQL and MySQL databases, write migrations, check their query plans and run
            them with your group's permissions.
          </p>
        </div>
      </aside>
      <main className="flex flex-1 items-center justify-center px-4 py-10 sm:px-6 sm:py-16">
        <section
          aria-labelledby="auth-title"
          className={`${styles.panel} w-full max-w-md p-6 sm:p-8`}
        >
          <h1 id="auth-title" className={styles.heading}>
            Sign in
          </h1>
          {children}
        </section>
      </main>
    </div>
  );
}
