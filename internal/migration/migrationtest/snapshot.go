package migrationtest

import (
	"context"

	"sqlwise/internal/schemadiff"
)

// Snapshots reads live schemas of mapped target pools.
type Snapshots struct{ Targets *Targets }

func (s Snapshots) Snapshot(ctx context.Context, databaseID, schema string) (schemadiff.Snapshot, error) {
	return s.Targets.Snapshot(ctx, databaseID, schema)
}
