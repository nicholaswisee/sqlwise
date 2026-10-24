package views

import "fmt"

// DatabaseNav identifies the target page and the signed-in user.
type DatabaseNav struct {
	ID        string
	Name      string
	CanEdit   bool
	Username  string
	CSRFToken string
	Active    string
	// Engine labels the DBMS; Track and TrackEarlier draw the version track.
	Engine       string
	Track        []TrackStop
	TrackEarlier int
}

type MigrationForm struct {
	Title string
	SQL   string
}

type MigrationDraftRow struct {
	ID             string
	Title          string
	Revision       int64
	AppliedVersion int64
	LastStatus     string
	UpdatedBy      string
	UpdatedAt      string
}

type MigrationAttemptRow struct {
	ID               string
	MigrationID      string
	MigrationTitle   string
	DraftRevision    int64
	CandidateVersion int64
	AppliedVersion   int64
	Status           string
	Executor         string
	StartedAt        string
	FinishedAt       string
	Reconciled       bool
	Error            string
	FailedStatement  int
	StatementCount   int
	SQL              string
	SQLHash          string
}

type MigrationHistoryData struct {
	Nav            DatabaseNav
	CurrentVersion int64
	Drafts         []MigrationDraftRow
	Attempts       []MigrationAttemptRow
	Form           MigrationForm
	Error          string
}

type MigrationDraftView struct {
	ID             string
	Title          string
	SQL            string
	SQLHash        string
	Dialect        string
	Revision       int64
	AppliedVersion int64
	CreatedBy      string
	CreatedAt      string
	UpdatedBy      string
	UpdatedAt      string
}

type ExecutionResultView struct {
	DatabaseID  string
	MigrationID string
	Attempt     *MigrationAttemptRow
	Message     string
}

type MigrationDetailData struct {
	Nav            DatabaseNav
	CurrentVersion int64
	Draft          MigrationDraftView
	Form           MigrationForm
	Attempts       []MigrationAttemptRow
	Blocked        string
	ReconcileURL   string
	Notice         string
	EditError      string
	Result         *ExecutionResultView
}

type AttemptDetailData struct {
	Nav     DatabaseNav
	Attempt MigrationAttemptRow
}

type SchemaDiffChange struct {
	Text        string
	Destructive bool
}

type SchemaDiffProposal struct {
	Schema            string
	SourceFingerprint string
	Changes           []SchemaDiffChange
	Warnings          []string
	SQL               string
	RequiresReview    bool
}

type SchemaDiffData struct {
	Nav      DatabaseNav
	Desired  string
	Title    string
	Proposal *SchemaDiffProposal
	Error    string
}

// DesiredSnapshotExample seeds the desired snapshot editor.
const DesiredSnapshotExample = `{
  "dialect": "postgresql",
  "schema": "public",
  "tables": [
    {
      "name": "customers",
      "primaryKey": ["id"],
      "columns": [
        {"name": "id", "type": "bigint", "nullable": false},
        {"name": "email", "type": "varchar(320)", "nullable": false},
        {"name": "created_at", "type": "timestamptz", "nullable": false, "default": "now()"}
      ]
    }
  ]
}`

func migrationsURL(databaseID string) string {
	return "/databases/" + databaseID + "/migrations"
}

func migrationURL(databaseID, migrationID string) string {
	return migrationsURL(databaseID) + "/" + migrationID
}

func attemptURL(databaseID, migrationID, attemptID string) string {
	return migrationURL(databaseID, migrationID) + "/attempts/" + attemptID
}

func schemaDiffURL(databaseID string) string {
	return "/databases/" + databaseID + "/schema-diff"
}

func statusLabel(status string) string {
	switch status {
	case "running":
		return "Running"
	case "succeeded":
		return "Succeeded"
	case "failed":
		return "Failed, rolled back"
	case "partially_applied":
		return "Partially applied"
	case "unknown":
		return "Outcome unknown"
	}
	return "Not executed"
}

func statusClass(status string) string {
	switch status {
	case "succeeded":
		return wsChipGreen
	case "failed":
		return wsChipRed
	case "running":
		return wsChipBlue
	case "partially_applied", "unknown":
		return wsChipAmber
	}
	return wsChip
}

func draftState(d MigrationDraftRow) (string, string) {
	if d.AppliedVersion > 0 {
		return fmt.Sprintf("Applied as v%d", d.AppliedVersion), statusClass("succeeded")
	}
	if d.LastStatus == "" {
		return "Draft", statusClass("")
	}
	return "Draft, last attempt: " + statusLabel(d.LastStatus), statusClass(d.LastStatus)
}

func version(v int64) string {
	if v == 0 {
		return "none"
	}
	return fmt.Sprintf("v%d", v)
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func itoa64(v int64) string { return fmt.Sprint(v) }

// errorSwap lets HTMX render 4xx/5xx result fragments instead of dropping them.
const errorSwap = "if (event.detail.xhr.status >= 400) { event.detail.shouldSwap = true; event.detail.isError = false; }"
