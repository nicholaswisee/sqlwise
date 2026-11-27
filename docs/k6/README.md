# k6 stress test

## Workload and measurements

[`run.sh`](../../tests/k6/run.sh) generates approved `CREATE TABLE` migrations through the real review API, then invokes [`stage.js`](../../tests/k6/stage.js) once per stage. The team defines normal load as **4 virtual users (VUs)** and 4 approved migrations. Concurrency steps through **1 → 4 → 8 → 16 → 4 VUs** with one job per distinct target database. A final 2-VU wave submits two requests approved against the same database head to verify that at most one can succeed. Each VU reads the jobs API, submits its approved migration, and polls the job until it reaches a terminal state or times out. **HTTP `202` means accepted, not successfully migrated.**

Each stage enforces **API p95 < 500 ms** and **unexpected HTTP error rate < 1%**. Raw HTTP failure rate includes expected HTTP conflicts; the threshold treats them as non-errors but counts unexpected responses and transport failures. API duration and request throughput include setup login and polling, not fixture creation. Queue wait is `started_at − queued_at`, worker execution is `finished_at − started_at`, and observed completion runs from submission to the terminal poll. Job throughput divides the observed terminal or successful count by the time from the end of setup to the last terminal observation (or total runner time for the k6 invocation if no job is observed terminal). An overload breach is recorded without skipping the recovery wave; the runner still exits nonzero. It defers fixture-table cleanup when work is unaccounted for.

## Run the benchmark

Run from the repository root in a fresh shell with Docker Compose, Go, Python 3, OpenSSL, and [k6 v1.7.1](https://github.com/grafana/k6/releases/tag/v1.7.1) on `PATH` (or set `K6_BIN`). Leave the target settings at their disposable defaults. Use a fresh disposable Compose project with ports `18082` and `18084` free, and choose a unique prefix of 1–12 lowercase letters or digits. The admin password is set only when the platform database is first initialized; a new prefix alone does not reset existing volumes.

```sh
export K6_RUN_PREFIX=k6run1
export SQLWISE_ADMIN_PASSWORD="$(openssl rand -hex 24)"
export K6_EDITOR_PASSWORD="$(openssl rand -hex 24)"
export K6_LEADER_PASSWORD="$(openssl rand -hex 24)"
docker build --target app -t sqlwise-wise-m2:local .
docker compose --project-name sqlwise-wise-k6 -f compose.yaml -f tests/k6/compose.override.yaml up --no-build -d --wait app worker postgres target
BASE_URL=http://127.0.0.1:18082 bash tests/k6/run.sh
```

The runner writes stage fixtures, logs, and JSON summaries to `/tmp/sqlwise-k6-$K6_RUN_PREFIX` by default; set `K6_OUT_DIR` to use another output directory. Review every stage summary and confirm all jobs are accounted for before removing the isolated project. **`down -v` destroys the synthetic project's database volumes. Do not run this benchmark against production.**

```sh
docker compose --project-name sqlwise-wise-k6 -f compose.yaml -f tests/k6/compose.override.yaml down -v
```

## Recorded results: 27 November 2026 (`m2nov27a`)

Tested revision `3860200aad56e36265c98f94bd4c20e919108456` with k6 v1.7.1 and a fresh local Compose stack: one API, one worker (four execution slots), and PostgreSQL 17.11 for the platform and target. The 16 target databases shared one target PostgreSQL instance. The load generator ran on the same Linux host (Intel i7-13620H, 16 logical CPUs, 31 GiB RAM).

| Stage (VUs / DBs)                  | API p50 / p95 (ms) | Requests/s | Raw / unexpected errors | k6 thresholds |
| ---------------------------------- | -----------------: | ---------: | ----------------------: | ------------- |
| [Baseline][k6-b] (1 / 1)           |      11.30 / 86.68 |       8.13 |                 0% / 0% | Pass          |
| [Normal][k6-n] (4 / 4)             |      12.53 / 86.25 |      30.43 |                 0% / 0% | Pass          |
| [Overload 1][k6-o] (8 / 8)         |      21.90 / 47.89 |      50.82 |                 0% / 0% | Pass          |
| [Overload 2][k6-p] (16 / 16)       |  33.83 / 60,000.94 |       1.57 |         16.67% / 16.67% | Fail: both    |
| [Recovery][k6-r] (4 / 4)           |      11.82 / 47.49 |      35.48 |                 0% / 0% | Pass          |
| [Same-head conflict][k6-c] (2 / 1) |      10.63 / 54.57 |      13.21 |                 0% / 0% | Pass          |

| Stage              | HTTP `202` | Succeeded / failed | Queue p50 / p95 (s) | Worker p50 / p95 (s) | Observed completion p50 / p95 (s) | Terminal / successful jobs/s |
| ------------------ | ---------: | -----------------: | ------------------: | -------------------: | --------------------------------: | ---------------------------: |
| Baseline           |          1 |              1 / 0 |       0.490 / 0.490 |        0.106 / 0.106 |                     0.859 / 0.859 |                  1.15 / 1.15 |
| Normal             |          4 |              4 / 0 |       0.200 / 0.203 |        0.096 / 0.113 |                     0.556 / 0.558 |                  6.97 / 6.97 |
| Overload 1         |          8 |              8 / 0 |       0.565 / 0.596 |        0.049 / 0.090 |                     0.887 / 0.890 |                  8.90 / 8.90 |
| Overload 2         |          1 |              1 / 0 |       0.048 / 0.048 |        0.021 / 0.021 |                   60.309 / 60.309 |                  0.02 / 0.02 |
| Recovery           |          4 |              4 / 0 |       0.377 / 0.378 |        0.067 / 0.071 |                     0.561 / 0.565 |                  6.97 / 6.97 |
| Same-head conflict |          2 |   1 / 1 (expected) |       0.710 / 0.940 |        0.037 / 0.054 |                     0.806 / 1.037 |                  1.87 / 0.94 |

### Stage-by-stage analysis

- **Baseline:** The single migration succeeded. The queue and completion percentiles each describe one job, not a latency distribution.
- **Normal:** All four migrations finished, with 0.203 s queue p95. This is the comparison point for recovery.
- **Overload 1:** All eight finished. Successful jobs/s rose from 6.97 to 8.90, but queue p95 rose from 0.203 s to 0.596 s even as API p95 fell. API latency alone would miss the extra queueing delay.
- **Overload 2:** 15 submissions and one poll timed out (16 of 96 HTTP requests). Reconciliation found no job for any timed-out submission, and a post-run platform database check confirmed no job rows for those 15 requests. The only accepted job succeeded; its 60.309 s observed completion includes the timed-out poll, while worker execution took 0.021 s. The runner deferred fixture cleanup for this wave and exited `1` after recovery and conflict. The underlying cause of the request stall was not established.
- **Recovery:** All four migrations succeeded at 6.97 jobs/s again, with no HTTP errors. Queue p95 was still higher than at normal load (0.378 s vs. 0.203 s), while observed completion p95 was close (0.565 s vs. 0.558 s).
- **Same-head conflict:** Both submissions got `202`, but only one migration succeeded. The other ended in the expected stale-head failure with no database effect. Neither was an HTTP failure.

These are short bursts of synthetic DDL, not a sustained capacity test. The same-head wave checks conflict handling, not throughput for multiple successful migrations to one database. [Earlier results](https://github.com/nicholaswisee/sqlwise/blob/6bc61744b7c2645b2a05f15f0bce890e2e36690a/README.md#k6-queue-benchmark-disposable-compose-project) on a different revision include both a passing and a failing 16-VU wave. The linked JSON summaries for this run retain aggregate metrics and threshold results only; they omit fixtures, logs, and session data.

[k6-b]: m2nov27a/baseline-summary.json
[k6-n]: m2nov27a/normal-summary.json
[k6-o]: m2nov27a/overload1-summary.json
[k6-p]: m2nov27a/overload2-summary.json
[k6-r]: m2nov27a/recovery-summary.json
[k6-c]: m2nov27a/conflict-summary.json

## Prepare and reset fixtures

Use the fixture CLI directly only against a disposable setup. Replace `id1,id2` with registered database IDs, supply the editor and leader credentials locally, and set `K6_TARGET_URL` to the disposable target DSN before running cleanup.

```sh
K6_EDITOR=editor:password K6_LEADERS=lead1:password,lead2:password \
  go run ./cmd/k6fixture prepare -url http://127.0.0.1:18082 -databases id1,id2 -per-database 1 -run r1 > fixtures.json
K6_TARGET_URL="${K6_TARGET_URL:?Set this to the disposable target DSN}" \
  go run ./cmd/k6fixture reset -run r1
```

Each fixture gives `execute_path`, `request_id` and a unique `idempotency_key`. Requests prepared together for one database share a head, so only the first can succeed; the others are the expected stale-head conflicts. `reset` drops the run's `k6_` tables on the target; platform history is append-only and is not deleted.
