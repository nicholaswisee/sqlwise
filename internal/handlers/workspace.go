package handlers

import (
	"context"
	"fmt"

	"sqlwise/internal/database"
	"sqlwise/internal/migration"
	"sqlwise/internal/web/views"
)

// WorkspaceHeader supplies the engine label and the version track shown on
// every database page. Failures leave the header without them.
type WorkspaceHeader func(ctx context.Context, databaseID string) (engine string, track []views.TrackStop, earlier int)

func NewWorkspaceHeader(migrations *migration.Service, resolver database.Resolver) WorkspaceHeader {
	return func(ctx context.Context, databaseID string) (string, []views.TrackStop, int) {
		var engine string
		if resolver != nil {
			if d, err := resolver.DBMS(ctx, databaseID); err == nil {
				engine = d.Label()
			}
		}
		if migrations == nil {
			return engine, nil, 0
		}
		h, err := migrations.History(ctx, databaseID)
		if err != nil {
			return engine, nil, 0
		}
		track, earlier := versionTrack(databaseID, h)
		return engine, track, earlier
	}
}

const trackStops = 6

func versionTrack(databaseID string, h migration.History) ([]views.TrackStop, int) {
	base := "/databases/" + databaseID + "/migrations"
	applied := map[int64]migration.Attempt{}
	failures := map[int64]int{}
	for _, a := range h.Attempts {
		switch a.Status {
		case migration.StatusSucceeded:
			applied[a.AppliedVersion] = a
		case migration.StatusFailed:
			failures[a.CandidateVersion]++
		}
	}
	var stops []views.TrackStop
	for v := int64(1); v <= h.CurrentVersion; v++ {
		stop := views.TrackStop{Label: fmt.Sprintf("v%d", v), State: "applied", Href: base, Aria: fmt.Sprintf("Version %d", v)}
		if a, ok := applied[v]; ok {
			stop.Href = base + "/" + a.MigrationID + "/attempts/" + a.ID
			stop.Aria = fmt.Sprintf("Version %d, %s", v, a.MigrationTitle)
		}
		if v == h.CurrentVersion {
			stop.State, stop.Label, stop.Aria = "current", stop.Label+" now", stop.Aria+", current"
		}
		if n := failures[v]; n > 0 {
			stop.Note, stop.NoteTone = tries(n), "fail"
		}
		stops = append(stops, stop)
	}
	next := h.CurrentVersion + 1
	if len(h.Attempts) > 0 {
		last := h.Attempts[0]
		if last.CandidateVersion == next && last.ReconciledAt == nil {
			switch last.Status {
			case migration.StatusPartiallyApplied, migration.StatusUnknown, migration.StatusRunning:
				note := map[migration.Status]string{migration.StatusPartiallyApplied: "partial", migration.StatusUnknown: "unknown", migration.StatusRunning: "running"}[last.Status]
				state := "partial"
				if last.Status != migration.StatusPartiallyApplied {
					state = "unknown"
				}
				stops = append(stops, views.TrackStop{Label: fmt.Sprintf("v%d", next), Note: note, NoteTone: "warn", State: state,
					Href: base + "/" + last.MigrationID + "/attempts/" + last.ID, Aria: fmt.Sprintf("Version %d attempt, %s", next, note)})
				return trim(stops)
			}
		}
	}
	var draft *migration.Draft
	for i := range h.Drafts {
		d := &h.Drafts[i]
		if d.AppliedVersion == 0 && (draft == nil || d.UpdatedAt.After(draft.UpdatedAt)) {
			draft = d
		}
	}
	if draft != nil {
		stop := views.TrackStop{Label: fmt.Sprintf("v%d draft", next), State: "draft", Href: base + "/" + draft.ID, Aria: fmt.Sprintf("Draft for version %d, %s", next, draft.Title)}
		if n := failures[next]; n > 0 {
			stop.Note, stop.NoteTone = tries(n), "fail"
		}
		stops = append(stops, stop)
	}
	return trim(stops)
}

func tries(n int) string {
	if n == 1 {
		return "1 failed try"
	}
	return fmt.Sprintf("%d failed tries", n)
}

func trim(stops []views.TrackStop) ([]views.TrackStop, int) {
	if len(stops) <= trackStops {
		return stops, 0
	}
	earlier := len(stops) - trackStops
	return stops[earlier:], earlier
}

func (header WorkspaceHeader) fill(ctx context.Context, nav *views.DatabaseNav) {
	if header != nil {
		nav.Engine, nav.Track, nav.TrackEarlier = header(ctx, nav.ID)
	}
}
