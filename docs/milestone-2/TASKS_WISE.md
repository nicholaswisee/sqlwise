# Wise — Milestone 2 checklist

**Owner:** Nicholas Wise Saragih Sumbayak (13524037)

**Status:** proposed plan with API observability foundation implemented in [PR #10](https://github.com/nicholaswisee/sqlwise/pull/10); not a W2/W3 sign-off record. See [observability implementation status](OBSERVABILITY_IMPLEMENTATION.md).

**Effort:** 40 total = 16 credited SPA baseline (user-reported) + 24 planned.

Authority: the team-maintained `SPEC_FREEZE.md`, especially §§2–3, 5–8. Assignment requirements override the freeze (§1). Check off only after evidence exists; the SPA credit is reported, not newly verified.

## Scope and boundaries

- [x] W0 — 16 points, SPA migration completed per user report, not newly verified here; final regression remains unchecked below. README identifies `web/spa` as active and legacy sources as retained.
- [ ] W1 — 3 points, group-leader administration and authorization policy seam.
- [ ] W2 — 6 points, JSON logging, redaction and tracing foundation, including application-wide SQL coverage.
- [ ] W3 — 8 points, Collector, metrics, reproducible Grafana provisioning/dashboard.
- [ ] W4 — 5 points, k6 harness, coordinated fixtures/runs and README analysis.
- [ ] W5 — 2 points, release evidence, early-submission coordination and SPA regression.
- Own `internal/auth/`, `internal/access/`, leader admin API/SPA, `internal/telemetry/` (new), `deploy/observability/` (new), `tests/k6/` (new), and W-specific tests/docs.
- Add telemetry calls in Wise-owned files. Jason and James instrument their own domain files against the agreed contract; do not edit their implementation to add calls.
- No ownership of Jason's jobs UI/runtime or James's migration/review/history UI/executor. No second permission evaluator.
- Shared integration is coordinated, not concurrent: Wise integrates `cmd/server/api.go`, `main.go`, `.github/workflows/ci.yml`, `compose.yaml`, `.env.example`, root manifests and shared SPA API/barrel files from reviewed patches. Agree edit windows; do not overwrite domain patches.
- Preserve MySQL/schema-diff/query-plan behavior and retained legacy callers/fixtures. M2 execution must not have a synchronous production bypass.

## W1 — leaders and policy seam (3)

- [ ] Confirm the group/leader schema and migration with James's approval-policy fingerprint consumer; assign a leader explicitly for legacy groups, never guess on upgrade.
- [ ] Implement one designated active group leader and admin selection through existing auth/access policy.
- [ ] Expose the group-grant policy snapshot James needs; groups with view and edit both count. Keep leader approval distinct from edit permission.
- [ ] Add backend unit tests for leader selection, inactive/missing leader and grants; integration tests for admin/API persistence and denial paths.
- [ ] Add responsive SPA admin flows and browser tests for selection, loading/error/empty states, unauthorized access and mobile use.
- [ ] Demonstrate one view-only leader can approve for their group but cannot submit/execute solely by leadership (coordinate end-to-end with James).
- [ ] Document upgrade behavior, admin operation and fixture/setup evidence.

## W2 — mandatory logging and tracing (6)

- [ ] Inventory API, worker, platform DB, target pool, auth/admin/access, viewer/metadata/query-plan and migration SQL seams with Jason and James (SPEC §7 first handoff).
- [ ] Implement shared `internal/telemetry/` slog JSON fields and OTel provider/middleware/hooks; support API and worker stdout/stderr and W3C context propagation contract.
- [ ] Redact credentials, tokens, sensitive literals/parameters, DSNs and unsafe driver errors before export; retain safe query structure/hash and correlation IDs. Never log session IDs or sensitive rows.
- [ ] Log actual statement text (exact for non-sensitive SQL, explicit masking for sensitive literals/parameters), not a hash-only substitute. Assert successful/failed/retried statement coverage and secret-canary absence per SPEC §5; seek Q&A clarification for literal-exact sensitive copies.
- [ ] Cover start/result/commit/rollback, query duration/rows/error/SQLSTATE when available, actor, database, correlation and relevant migration/job/execution/attempt/batch IDs. Include system actor for background/startup SQL.
- [ ] Add unit tests for field serialization and redaction including secret canaries; integration tests capture successful, failed, retry and read-only SQL events and correlated spans.
- [ ] Coordinate Jason trace extraction/queue propagation and pool/queue hooks; James execution SQL/attempt hooks. No secrets in baggage.
- [ ] Supply a short local runbook and sanitized correlated API→job→worker→query success/failure examples. Never assert complete coverage without inventory evidence.

## W3 — observability bonus (8)

- [ ] Implement bounded-cardinality Prometheus metrics for job states, retries, queue/lock wait, logical migration duration, committed batches/rows and platform/target pool active/idle/wait; see SPEC §5 table.
- [ ] Add/export measurements in API and worker where relevant; domain owners expose real values from their own queue/DB lifecycle. Do not label by user/job/execution ID, target name, SQL or error text.
- [ ] Add unit tests for metric semantics/cardinality and integration tests that exercise queue, completion/failure, pool and batch paths, then scrape actual endpoints.
- [x] Create repo-provisioned `deploy/observability/` configuration for Collector logs+traces, Prometheus, Loki, Tempo, Grafana datasources/dashboard, preferably Compose profile/override; check image/config compatibility. API path verified in PR #10; worker integration remains below.
- [ ] Ensure Collector receives API and worker JSON stdout/stderr, not traces alone. Ensure a fresh stack reproduces panels without manual setup.
- [ ] Include required dashboard views: job status, queue wait, migration duration, connections active/idle, plus retries, lock/batch/rows and pool wait.
- [ ] Test fresh-stack replay and scrape; save sanitized dashboard/metrics and correlated log/trace sample. Document startup, shutdown and troubleshooting; no live external Grafana edits are required.

## W4 — k6 bonus (5)

- [ ] Agree with Jason/James on approved-request fixture creation, unique requests, job completion fields and reset/cleanup helpers. Never bypass authorization or approval for throughput.
- [ ] Add reproducible k6 scripts under `tests/k6/` for authorized concurrent API reads and approved jobs on same and different databases.
- [ ] Separate simultaneous same-database approved-request conflicts from successful fresh-head sequential submissions; keep reads and independent cross-database jobs concurrent. Report expected stale-head rejection, raw HTTP error rate and unexpected errors separately; rejected/failed jobs never count as migration throughput (SPEC §6).
- [ ] Define environment and normal load; run baseline → normal → at least two above-normal stress stages → recovery. Track p50/p95, throughput, HTTP errors, queue wait, execution/completed-job throughput, failures and drain time.
- [ ] Use freeze targets p95 <500ms and unexpected HTTP errors <1% as targets, not claimed results; report overload threshold failures honestly.
- [ ] Track accepted job IDs to terminal/recovery-blocked outcomes in a documented observation timeout. A 202 is acceptance only, not execution success.
- [ ] Drain/account for jobs at every stage. Analyze same-DB serialization, cross-DB parallelism, resource limits and recovery.
- [ ] Save sanitized raw summaries and script instructions. Add README explanation, thresholds, results and per-stage analysis (links may supplement, not replace analysis).

## W5 — release, regression and early submission (2)

- [ ] Re-run SPA regression at 1440, 390 and 320px, same-origin navigation/refresh, login/session/CSRF/access/admin and all new owner-provided screens. James reviews regression evidence.
- [ ] Coordinate all-member requirement/evidence signoff; do not record approval on anyone's behalf. Confirm continuing GitHub repository/form access before deadline.
- [ ] Coordinate official early submission by **23 Nov 2026, 13:10 WIB**; submit only after team signoff and save actual confirmation. Monitor Q&A/spec changes; no invented demo/form details.
- [ ] Ensure README/release docs include contribution/evidence matrix, setup, worker/observability/test/benchmark/security instructions and known gaps.

## Gates and handoffs

- [ ] 9–10 Nov: confirm leader policy and telemetry DTOs; agree shared-file windows. Receive complete SQL/queue/pool inventory from all.
- [ ] 10 Nov: deliver James the leader/grant fingerprint and fresh durable-actor authorization contract; deliver Jason trace/metric hook points.
- [ ] 11–12 Nov: leader admin and JSON/trace foundation; coordinate first queued normal migration with Jason/James.
- [ ] If the complete queued slice misses end of 12 Nov, convene the 13 Nov blocker/rebalance review. A J4 jobs-SPA fallback transfer requires all-member agreement and revised points; it is not an extra default frontend duty (SPEC §2).
- [ ] 13–16 Nov: Collector/metrics/dashboard and basic correlation; re-estimate together on 12 and 16 Nov as freeze §2 requires.
- [ ] 17–18 Nov: actual k6 overload/recovery runs and README analysis; give James review opportunity on regression/evidence.
- [ ] 19 Nov consolidate Q&A questions; submit by **20 Nov, 13:10 WIB**. Reconcile official answers with all owners.
- [ ] 20–22 Nov: reproduce dashboard from fresh stack, integrated checks, release candidate and early-submission signoff.
- [ ] After submission: preserve confirmation and provide blockers/evidence for the **27 Nov, 13:10 WIB** normal deadline.

## Acceptance evidence

- [ ] W1 success/failure/access-denial unit + real-component integration tests; responsive browser coverage; upgrade/admin runbook.
- [ ] W2 API/worker/read-only/platform/target SQL coverage inventory, secret-leak tests and correlated trace/log examples.
- [ ] W3 actual API/worker metrics, bounded labels, fresh provisioning replay, dashboard/sample and runbook.
- [ ] W4 reproducible approved fixture, staged stress/recovery results, drained-job accounting and README stage analysis.
- [ ] W0 M1 login/session/CSRF/access/database-management regression, legacy checks (`go tool templ generate`, `vp check`, `vp build`) and SPA browser checks retained per README.
- [ ] Cross-domain acceptance includes stale/revoked access after enqueue, cross-database child IDs denied, no target writes on denial, and no synchronous migration path.
- [ ] Add/maintain README commands only after verifying they work; freeze §8 lists existing Go and SPA checks but none are claimed run here.

**Not claimed:** full W2/W3 acceptance, CodeQL review, team approval or submission. Follow the team-maintained `SPEC_FREEZE.md` §§8–9 for release verification/change control.
