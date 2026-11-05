export const styles = {
  focus: "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-cyan-700",
  panel: "rounded-xl border border-slate-200 bg-white",
  heading: "text-2xl font-semibold tracking-tight text-slate-900",
  muted: "text-sm text-slate-600",
  link: "text-cyan-700 underline underline-offset-4 hover:text-cyan-800 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-cyan-700",
  button:
    "inline-flex min-h-11 items-center justify-center gap-2 rounded-lg border border-slate-300 bg-white px-4 text-sm font-medium text-slate-900 no-underline hover:bg-slate-50 active:bg-slate-100 disabled:cursor-wait disabled:opacity-60 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-cyan-700",
  primary:
    "inline-flex min-h-11 items-center justify-center gap-2 rounded-lg border border-cyan-700 bg-cyan-700 px-4 text-sm font-semibold text-white no-underline hover:bg-cyan-800 active:bg-cyan-900 disabled:cursor-wait disabled:opacity-60 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-cyan-700",
  input:
    "min-h-11 w-full rounded-lg border border-slate-300 bg-white px-3 text-base text-slate-900 hover:border-slate-400 focus-visible:border-cyan-700 focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-cyan-700",
  label: "block text-sm font-medium text-slate-800",
  chip: "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full bg-slate-100 px-2.5 py-0.5 text-xs font-medium text-slate-700 ring-1 ring-slate-200",
  chipTeal:
    "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full bg-cyan-50 px-2.5 py-0.5 text-xs font-medium text-cyan-900 ring-1 ring-cyan-200",
  alert: "rounded-lg border border-rose-200 bg-rose-50 p-4 text-sm leading-6 text-rose-900",
} as const;
