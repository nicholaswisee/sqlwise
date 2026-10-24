package views

import "strings"

// Light workspace style recipes shared by every database page. Tailwind
// scans this directory, so the classes below are compiled like any template.
const (
	wsPage      = "min-h-screen bg-slate-100 text-slate-900"
	wsPanel     = "rounded-xl border border-slate-200 bg-white"
	wsHeading   = "text-2xl font-semibold tracking-tight text-slate-900"
	wsSubhead   = "text-lg font-semibold text-slate-900"
	wsMuted     = "text-sm text-slate-600"
	wsLabel     = "block text-sm font-medium text-slate-800"
	wsLink      = "text-cyan-700 underline underline-offset-4 hover:text-cyan-800 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-cyan-700"
	wsFocus     = "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-cyan-700"
	wsButton    = "inline-flex min-h-11 items-center justify-center gap-2 rounded-lg border border-slate-300 bg-white px-4 text-sm font-medium text-slate-900 no-underline hover:bg-slate-50 active:bg-slate-100 disabled:cursor-wait disabled:opacity-60 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-cyan-700"
	wsPrimary   = "inline-flex min-h-11 items-center justify-center gap-2 rounded-lg border border-cyan-700 bg-cyan-700 px-4 text-sm font-semibold text-white no-underline hover:bg-cyan-800 active:bg-cyan-900 disabled:cursor-wait disabled:opacity-60 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-cyan-700"
	wsWarnBtn   = "inline-flex min-h-11 items-center justify-center gap-2 rounded-lg border border-amber-800 bg-amber-800 px-4 text-sm font-semibold text-white hover:bg-amber-900 disabled:cursor-wait disabled:opacity-60 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-amber-800"
	wsInput     = "min-h-11 w-full rounded-lg border border-slate-300 bg-white px-3 text-base text-slate-900 hover:border-slate-400 focus-visible:border-cyan-700 focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-cyan-700"
	wsSelect    = "min-h-11 rounded-lg border border-slate-300 bg-white px-3 text-sm text-slate-900 hover:border-slate-400 focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-cyan-700"
	wsTextarea  = "w-full rounded-lg border border-slate-300 bg-slate-50 p-3 font-mono text-sm leading-6 text-slate-900 hover:border-slate-400 focus-visible:border-cyan-700 focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-cyan-700"
	wsCode      = "max-h-[32rem] overflow-auto rounded-lg border border-slate-200 bg-slate-50 p-4 font-mono text-sm leading-6 whitespace-pre text-slate-900"
	wsChip      = "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full bg-slate-100 px-2.5 py-0.5 text-xs font-medium text-slate-700 ring-1 ring-slate-200"
	wsChipTeal  = "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full bg-cyan-50 px-2.5 py-0.5 text-xs font-medium text-cyan-900 ring-1 ring-cyan-200"
	wsChipGreen = "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full bg-emerald-50 px-2.5 py-0.5 text-xs font-medium text-emerald-800 ring-1 ring-emerald-200"
	wsChipAmber = "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full bg-amber-50 px-2.5 py-0.5 text-xs font-medium text-amber-900 ring-1 ring-amber-300"
	wsChipRed   = "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full bg-rose-50 px-2.5 py-0.5 text-xs font-medium text-rose-800 ring-1 ring-rose-200"
	wsChipBlue  = "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full bg-sky-50 px-2.5 py-0.5 text-xs font-medium text-sky-800 ring-1 ring-sky-200"
	wsAlert     = "rounded-lg border border-rose-200 bg-rose-50 p-4 text-sm leading-6 text-rose-900"
	wsWarn      = "rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm leading-6 text-amber-950"
	wsSuccess   = "rounded-lg border border-emerald-200 bg-emerald-50 p-4 text-sm leading-6 text-emerald-900"
	wsNotice    = "rounded-lg border border-slate-200 bg-white p-4 text-sm leading-6 text-slate-800"
	wsEmpty     = "rounded-lg border border-dashed border-slate-300 bg-white p-6 text-sm text-slate-600"
	wsTable     = "w-full text-left text-sm tabular-nums"
	wsTh        = "whitespace-nowrap border-b border-slate-200 bg-slate-50 px-4 py-2.5 text-xs font-semibold text-slate-600"
	wsTd        = "border-b border-slate-100 px-4 py-2.5 align-top"
)

// TrackStop is one point on a database's version track: an applied version,
// a partial or unknown attempt, or the next draft.
type TrackStop struct {
	Label string
	Note  string
	// NoteTone is "", "warn" or "fail".
	NoteTone string
	// State is applied, current, draft, partial or unknown.
	State string
	Href  string
	Aria  string
}

func trackDotClass(state string) string {
	base := "relative z-10 block size-4 rounded-full "
	switch state {
	case "current":
		return base + "bg-cyan-700 ring-4 ring-cyan-100"
	case "draft":
		return base + "border-2 border-dashed border-cyan-700 bg-white"
	case "partial", "unknown":
		return base + "bg-amber-600 ring-4 ring-amber-100"
	}
	return base + "bg-cyan-700"
}

func trackNoteClass(tone string) string {
	switch tone {
	case "warn":
		return "text-[11px] leading-4 text-amber-900"
	case "fail":
		return "text-[11px] leading-4 text-rose-800"
	}
	return "text-[11px] leading-4 text-slate-600"
}

func initials(username string) string {
	parts := strings.FieldsFunc(username, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == ' ' })
	out := ""
	for _, p := range parts {
		if out == "" || len(parts) > 1 && len(out) < 2 {
			out += strings.ToUpper(string([]rune(p)[:1]))
		}
	}
	if out == "" {
		return "?"
	}
	return out
}
