# Migration jobs and recovery

This guide describes queued migration execution, recovery, and the disposable large-data benchmark.

## Workflow and worker behavior

Approved executions are queued durably and run by `cmd/worker`, never inside an HTTP request. `202 Accepted` means the job was stored, not that the migration succeeded; poll `GET /api/v1/databases/:id/jobs/:jobID` (or open **Jobs** in the workspace) for `status` (`queued`, `running`, `succeeded`, `failed`, `blocked`) and `effect` on the target (`none`, `committed`, `partial`, `unknown`), plus `attempts`, `queued_at`, `started_at`, `finished_at` and committed batches/rows.

- One worker at a time holds a target's execution gate (a session advisory lock on a dedicated target connection) for the whole job, so the same database is serialized, including between batch commits, while different databases run in parallel.
- Confirmed transient failures before any target write are retried at most 3 times in total, after 1 s and 2 s. SQL, access and stale-request failures are final.
- A batch migration ("Run as checkpointed batches") commits each batch with its checkpoint and saved row values in one target transaction. If it stops after committing only some batches, those changes stay and later jobs on that database wait for recovery. An approved **resume** finishes the migration so it can enter committed history; an approved **compensate** restores saved values newest batch first and stops rather than overwrite rows changed since.

## Worker settings

Worker settings use `WORKER_*` variables (`WORKER_ID`, `WORKER_CONCURRENCY`, `WORKER_LEASE`, `WORKER_LOCK_WAIT`, `WORKER_JOB_TIMEOUT`, `WORKER_SHUTDOWN_GRACE`, `WORKER_RECHECK`, `WORKER_METRICS_ADDR`); the worker serves `/metrics` and `/health` on port 9091.

## Recovery runbook

| Situation                                    | What happens                                                                                                                                                                       | Action                                                                                                                                              |
| -------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| Worker died                                  | Its target session ends, releasing the gate. After the lease expires another worker reclaims the job and checks the target receipt or batch ledger instead of re-running SQL.      | None; a restart with the same `WORKER_ID` reclaims at once.                                                                                         |
| Worker stalled but alive                     | Another worker claims the expired job but waits for the gate, without spending attempts.                                                                                           | Stop the stalled process if it is hung.                                                                                                             |
| Target unreachable before execution          | Confirmed no-effect connection failures are retried after 1 s and 2 s.                                                                                                             | Restore connectivity; if the job already failed, create a new request.                                                                              |
| Outcome unknown                              | `blocked/unknown`; the worker reacquires the target lock and checks the receipt or batch checkpoints. It does not repeat the SQL or offer recovery while the outcome is uncertain. | Restore connectivity if the target stays unreachable.                                                                                               |
| Batch stopped with confirmed partial changes | `blocked/partial`; later jobs on that database wait until recovery succeeds.                                                                                                       | Request resume or compensation from the job page and get it approved. If any batches have already been compensated, only compensation can continue. |

## Large-data benchmark

Large-data benchmark (`cmd/batchbench`, disposable database only):

```sh
docker compose up -d --wait postgres-test
go run ./cmd/batchbench -admin-url 'postgres://sqlwise_test:sqlwise_test_only@localhost:5435/sqlwise_test?sslmode=disable' \
  -rows 1000000 -batch 50000 -fail-at 8
```

Recorded on 21 November 2026: Docker Desktop on WSL2, host with 28 CPUs and 31 GiB RAM, no container limits, `postgres:17-alpine` (PostgreSQL 17.11, default settings, `fsync` and `synchronous_commit` on). Table `bench.accounts (id bigint PRIMARY KEY, status text, balance numeric(12,2), email text)`, 1,000,000 rows (189 MB), only the primary-key index.

| Measurement                                                 | Result                                                        |
| ----------------------------------------------------------- | ------------------------------------------------------------- |
| Whole-table `UPDATE bench.accounts SET status = 'archived'` | 2.11 s (500,000 rows: 1.11 s)                                 |
| Same dataset, 50,000-row batches                            | 20.70 s, 20 batches, 1,000,000 rows                           |
| Peak Go heap during the batched run                         | 1.2 MB                                                        |
| Compensation of all 20 batches (reverse order)              | 3.59 s, every row restored                                    |
| Failure injected before batch 8                             | 7 batches / 350,000 rows stayed committed, the rest untouched |
| Resume on a new gate                                        | started at batch 8, 13.30 s, each row updated exactly once    |

Batching is about 10× slower than one statement because every batch also stores before/after values and a checkpoint and pays its own commit. In exchange each transaction is bounded, progress survives failures, and committed work can be resumed or compensated. Memory stays flat because rows never leave the server. One run per size on a shared developer machine; timings vary with disk and WAL.
