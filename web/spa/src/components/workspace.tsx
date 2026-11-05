import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { styles } from "./styles";

export function Brand({ href = "/databases" }: { href?: string }) {
  return (
    <Link
      to={href}
      className={`inline-flex min-h-11 items-center gap-2 text-base font-bold tracking-tight text-cyan-900 ${styles.focus}`}
    >
      <svg
        width="22"
        height="22"
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
  );
}

export function WorkspaceTopbar({
  username,
  children,
}: {
  username: string;
  children?: ReactNode;
}) {
  const parts = username.split(/[._\-\s]+/u).filter(Boolean);
  const initials =
    parts
      .slice(0, 2)
      .map((part) => part.charAt(0))
      .join("")
      .toUpperCase() || "?";
  const accountPath: string = "/account";
  return (
    <header className="border-b border-slate-200 bg-white">
      <div className="mx-auto flex w-full max-w-7xl flex-wrap items-center gap-x-6 gap-y-1 px-4 py-1.5 sm:px-6">
        <Brand />
        {children}
        <Link
          to={accountPath}
          className={`ml-auto inline-flex min-h-11 items-center gap-2 text-sm text-slate-700 hover:text-cyan-800 ${styles.focus}`}
        >
          <span
            aria-hidden="true"
            className="inline-flex size-8 shrink-0 items-center justify-center rounded-full bg-cyan-700 text-xs font-semibold text-white"
          >
            {initials}
          </span>
          <span className="break-all">
            Signed in as <strong className="font-semibold text-slate-900">{username}</strong>
          </span>
        </Link>
      </div>
    </header>
  );
}

export interface TrackStop {
  label: string;
  note?: string;
  note_tone?: "" | "warn" | "fail";
  state: "applied" | "current" | "draft" | "partial" | "unknown";
  href: string;
  aria: string;
}

export interface WorkspaceMetadata {
  id: string;
  name: string;
  can_edit: boolean;
  engine?: string;
  version_track: TrackStop[];
  track_earlier?: number;
}

export function WorkspaceShell({
  workspace,
  username,
  active,
  children,
}: {
  workspace: WorkspaceMetadata;
  username: string;
  active: "schema" | "migrations" | "schema-diff";
  children: ReactNode;
}) {
  const base = `/databases/${encodeURIComponent(workspace.id)}`;
  const databasesPath: string = "/databases";
  return (
    <div className="flex min-h-screen flex-col bg-slate-100 text-slate-900">
      <WorkspaceTopbar username={username}>
        <nav
          aria-label="Breadcrumb"
          className="flex min-w-0 flex-wrap items-center gap-2 text-sm text-slate-600"
        >
          <Link
            to={databasesPath}
            className={`inline-flex min-h-11 items-center text-slate-700 hover:text-cyan-800 ${styles.focus}`}
          >
            Databases
          </Link>
          <span aria-hidden="true">/</span>
          <span className="break-all font-medium text-slate-900">{workspace.name}</span>
        </nav>
      </WorkspaceTopbar>
      <section aria-label="Database" className="border-b border-slate-200 bg-white">
        <div className="mx-auto flex w-full max-w-7xl flex-wrap items-end justify-between gap-x-10 gap-y-5 px-4 pt-6 sm:px-6">
          <div className="flex min-w-0 flex-wrap items-center gap-2.5">
            <p className={`break-all ${styles.heading}`}>{workspace.name}</p>
            {workspace.engine && <span className={styles.chip}>{workspace.engine}</span>}
            <span className={workspace.can_edit ? styles.chipTeal : styles.chip}>
              {workspace.can_edit ? "View + Edit" : "View only"}
            </span>
          </div>
          <VersionTrack stops={workspace.version_track} earlier={workspace.track_earlier} />
        </div>
        <nav
          aria-label="Database sections"
          className="mx-auto mt-3 flex w-full max-w-7xl flex-wrap gap-1 px-4 sm:px-6"
        >
          {(
            [
              ["schema", "Schema", base],
              ["migrations", "Migrations", `${base}/migrations`],
              ["schema-diff", "Schema diff", `${base}/schema-diff`],
            ] as const
          ).map(([key, label, href]) => (
            <Link
              key={key}
              to={href}
              aria-current={active === key ? "page" : undefined}
              className={`-mb-px inline-flex min-h-11 items-center border-b-2 px-3 text-sm ${active === key ? "border-cyan-700 font-semibold text-cyan-900" : "border-transparent text-slate-600 hover:border-slate-300 hover:text-slate-900"} ${styles.focus}`}
            >
              {label}
            </Link>
          ))}
        </nav>
      </section>
      <main className="mx-auto w-full max-w-7xl flex-1 px-4 py-8 sm:px-6">{children}</main>
    </div>
  );
}

export function VersionTrack({ stops, earlier = 0 }: { stops: TrackStop[]; earlier?: number }) {
  return (
    <nav aria-label="Version history" className="max-w-full overflow-x-auto pb-1">
      {stops.length === 0 ? (
        <p className="pb-2 text-sm text-slate-600">No migrations yet</p>
      ) : (
        <ol className="flex items-start">
          {earlier > 0 && (
            <li className="mr-1 self-start pt-0.5 text-xs text-slate-600">+{earlier} earlier</li>
          )}
          {stops.map((stop, index) => (
            <li
              key={`${stop.href}-${index}`}
              className="relative flex w-20 shrink-0 flex-col items-center"
            >
              {index > 0 && (
                <span
                  aria-hidden="true"
                  className="absolute right-1/2 top-[7px] h-0.5 w-full bg-cyan-200"
                />
              )}
              <Link
                to={stop.href}
                aria-label={stop.aria}
                className={`flex flex-col items-center gap-1.5 rounded-md px-1 pb-1 text-xs text-slate-700 hover:text-cyan-900 ${styles.focus}`}
              >
                <span
                  className={`relative z-10 block size-4 rounded-full ${stop.state === "current" ? "bg-cyan-700 ring-4 ring-cyan-100" : stop.state === "draft" ? "border-2 border-dashed border-cyan-700 bg-white" : stop.state === "partial" || stop.state === "unknown" ? "bg-amber-600 ring-4 ring-amber-100" : "bg-cyan-700"}`}
                />
                <span
                  className={`font-mono ${stop.state === "current" || stop.state === "draft" ? "font-semibold text-slate-900" : ""}`}
                >
                  {stop.label}
                </span>
                {stop.note && (
                  <span
                    className={`text-[11px] leading-4 ${stop.note_tone === "warn" ? "text-amber-900" : stop.note_tone === "fail" ? "text-rose-800" : "text-slate-600"}`}
                  >
                    {stop.note}
                  </span>
                )}
              </Link>
            </li>
          ))}
        </ol>
      )}
    </nav>
  );
}
