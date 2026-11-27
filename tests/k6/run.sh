#!/usr/bin/env bash
set -euo pipefail

: "${BASE_URL:=http://127.0.0.1:18082}"
: "${K6_RUN_PREFIX:?set a unique 1-12 character lowercase/digit K6_RUN_PREFIX}"
: "${SQLWISE_ADMIN_USERNAME:=admin}"
: "${SQLWISE_ADMIN_PASSWORD:?set the disposable Compose admin password}"
: "${K6_EDITOR_PASSWORD:?set a synthetic editor password}"
: "${K6_LEADER_PASSWORD:?set a synthetic leader password}"
: "${K6_BIN:=k6}"
: "${K6_DATABASE_COUNT:=16}"
: "${K6_TARGET_DATABASE_PREFIX:=${K6_RUN_PREFIX}_db}"
: "${K6_TARGET_USERNAME:=target_owner}"
: "${K6_TARGET_PASSWORD:=target_owner_dev_only}"
: "${K6_TARGET_RESET_HOST:=127.0.0.1}"
: "${K6_TARGET_RESET_PORT:=18084}"
export SQLWISE_ADMIN_USERNAME K6_TARGET_DATABASE_PREFIX K6_TARGET_USERNAME K6_TARGET_PASSWORD
[[ "$K6_RUN_PREFIX" =~ ^[a-z0-9]{1,12}$ ]] || {
    echo 'K6_RUN_PREFIX must be 1-12 lowercase letters/digits' >&2
    exit 2
}
[[ "$K6_DATABASE_COUNT" =~ ^[0-9]+$ ]] && ((K6_DATABASE_COUNT >= 16)) || {
    echo 'K6_DATABASE_COUNT must be at least 16' >&2
    exit 2
}
command -v "$K6_BIN" >/dev/null
[[ "$("$K6_BIN" version)" == *v1.7.1* ]] || {
    echo 'requires official k6 v1.7.1' >&2
    exit 2
}

compose=(docker compose --project-name sqlwise-wise-k6 -f compose.yaml -f tests/k6/compose.override.yaml)
for ((index = 0; index < K6_DATABASE_COUNT; index++)); do
    "${compose[@]}" exec -T target createdb -U "$K6_TARGET_USERNAME" "${K6_TARGET_DATABASE_PREFIX}_${index}"
done

editor="k6e_${K6_RUN_PREFIX}"
leader="k6l_${K6_RUN_PREFIX}"
export BASE_URL K6_EDITOR_USERNAME="$editor" K6_EDITOR_PASSWORD
export K6_LEADER_USERNAME="$leader" K6_LEADER_PASSWORD
export K6_EDITOR="$editor:$K6_EDITOR_PASSWORD" K6_LEADERS="$leader:$K6_LEADER_PASSWORD"
export K6_RUN_PREFIX
out_dir="${K6_OUT_DIR:-/tmp/sqlwise-k6-$K6_RUN_PREFIX}"
mkdir -p "$out_dir"
databases="$(python3 tests/k6/bootstrap.py)"
IFS=, read -r -a db_ids <<<"$databases"
((${#db_ids[@]} >= 16)) || {
    echo 'bootstrap returned fewer than 16 PostgreSQL databases' >&2
    exit 1
}
runs=()

run_stage() {
    local stage="$1" db_count="$2" vus="$3" per_db="$4" conflict="$5"
    local suffix run_id list="" index started ended k6_status=0
    case "$stage" in baseline) suffix=b ;; normal) suffix=n ;; overload1) suffix=o ;; overload2) suffix=p ;; recovery) suffix=r ;; conflict) suffix=c ;; *)
        echo "unknown stage: $stage" >&2
        return 2
        ;;
    esac
    run_id="${K6_RUN_PREFIX}${suffix}"
    for ((index = 0; index < db_count; index++)); do
        [[ -z "$list" ]] || list+=,
        list+="${db_ids[index]}"
    done
    echo "stage=$stage databases=$db_count requests_per_database=$per_db vus=$vus"
    K6_EDITOR="$K6_EDITOR" K6_LEADERS="$K6_LEADERS" go run ./cmd/k6fixture prepare \
        -url "$BASE_URL" -databases "$list" -per-database "$per_db" -run "$run_id" >"$out_dir/$stage-fixtures.json"
    started=$(date +%s.%N)
    USERNAME="$editor" PASSWORD="$K6_EDITOR_PASSWORD" "$K6_BIN" run \
        -e BASE_URL="$BASE_URL" -e FIXTURES="$out_dir/$stage-fixtures.json" \
        -e VUS="$vus" -e EXPECT_CONFLICT="$conflict" \
        -e OUT="$out_dir/$stage-summary.json" tests/k6/stage.js 2>&1 | tee "$out_dir/$stage.log" || k6_status=$?
    ended=$(date +%s.%N)
    if ! python3 - "$out_dir/$stage-summary.json" "$stage" "$db_count" "$per_db" "$started" "$ended" "$k6_status" "$out_dir/benchmark-failure" "$out_dir/$stage-undrained" <<'PY'; then
import json, sys
path, stage, databases, per_db = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
runner_elapsed = float(sys.argv[6]) - float(sys.argv[5])
k6_status, marker, undrained_marker = int(sys.argv[7]), sys.argv[8], sys.argv[9]
data = json.load(open(path))['metrics']
def count(name): return int(data.get(name, {}).get('values', {}).get('count', 0))
def values(name): return data.get(name, {}).get('values', {})
def timing(name):
    v = values(name)
    return f"{v.get('avg', 0):.3f}/{v.get('med', 0):.3f}/{v.get('p(95)', 0):.3f}"
accepted, observed, succeeded = count('accepted_202'), count('accepted_jobs_observed'), count('terminal_succeeded')
failed, blocked, timed_out = count('terminal_failed'), count('recovery_blocked'), count('poll_timeout')
outstanding, stale = count('outstanding_jobs'), count('expected_terminal_stale_conflicts')
expected, unexpected = count('expected_http_errors'), count('unexpected_http_errors')
reads, bad_states = count('authorized_reads'), count('unexpected_job_states')
request_timeouts, submission_timeouts = count('request_timeouts'), count('submission_timeouts')
no_job, reconciliation_reads = count('no_job_after_submission_timeout'), count('reconciliation_reads')
transport_errors, http_requests = count('transport_errors'), count('http_reqs')
error_rate = values('unexpected_http_error_rate').get('rate', 0)
http_values = values('http_req_duration')
http_p95, http_p50 = http_values.get('p(95)', 0), http_values.get('med', 0)
wave_drain = values('wave_drain_seconds').get('max', 0)
threshold_breach = k6_status != 0 or error_rate >= 0.01 or http_p95 >= 500
if stage == 'conflict':
    drained = observed in (1, 2) and succeeded == 1 and failed == stale and stale <= 1 and expected + stale == 1
else:
    drained = observed == databases and succeeded == observed and failed == 0 and stale == 0 and expected == 0
accounting_failure = (not drained or blocked or timed_out or outstanding or bad_states or unexpected or
                      reads != databases * per_db or reconciliation_reads < submission_timeouts)
terminal = succeeded + failed + blocked
throughput_time = wave_drain or runner_elapsed
print(f"stage={stage} accepted_202={accepted} accepted_jobs_observed={observed} terminal_succeeded={succeeded} terminal_failed={failed} terminal_stale_conflicts={stale} recovery_blocked={blocked} poll_timeout={timed_out} request_timeouts={request_timeouts} submission_timeouts={submission_timeouts} no_job_after_timeout={no_job} outstanding={outstanding} expected_http_errors={expected} unexpected_http_errors={unexpected} transport_errors={transport_errors} http_requests={http_requests} unexpected_error_rate={error_rate:.4f} authorized_reads={reads} reconciliation_reads={reconciliation_reads} runner_elapsed_s={runner_elapsed:.3f} wave_drain_s={wave_drain:.3f} accepted_per_s={observed/max(throughput_time, 1e-9):.2f} terminal_per_s={terminal/max(throughput_time, 1e-9):.2f} success_per_s={succeeded/max(throughput_time, 1e-9):.2f} job_completion_avg/p50/p95_s={timing('job_completion_seconds')} queue_wait_avg/p50/p95_s={timing('queue_wait_seconds')} worker_execution_avg/p50/p95_s={timing('worker_execution_seconds')} http_req_duration_p50/p95_ms={http_p50:.3f}/{http_p95:.3f}")
if stage.startswith('overload'):
    if threshold_breach:
        print(f"stage={stage} load_threshold=BREACHED; continuing to recovery")
        open(marker, 'a').close()
    if accounting_failure:
        open(marker, 'a').close()
        open(undrained_marker, 'a').close()
elif threshold_breach or accounting_failure:
    raise SystemExit(f'{stage}: threshold or terminal accounting failed')
PY
        if [[ "$stage" != overload* ]]; then return 1; fi
        touch "$out_dir/benchmark-failure" "$out_dir/$stage-undrained"
    fi
    if [[ ! -e "$out_dir/$stage-undrained" ]]; then
        for ((index = 0; index < db_count; index++)); do
            K6_TARGET_URL="postgres://${K6_TARGET_USERNAME}:${K6_TARGET_PASSWORD}@${K6_TARGET_RESET_HOST}:${K6_TARGET_RESET_PORT}/${K6_TARGET_DATABASE_PREFIX}_${index}?sslmode=disable" \
                go run ./cmd/k6fixture reset -run "$run_id" 2>&1 | sed 's/dropped [0-9][0-9]* k6 tables/dropped tables/'
        done
    else
        echo "stage=$stage cleanup=deferred outstanding-or-unaccounted jobs" >&2
    fi
}

run_stage baseline 1 1 1 false
run_stage normal 4 4 1 false
run_stage overload1 8 8 1 false
run_stage overload2 16 16 1 false
run_stage recovery 4 4 1 false
run_stage conflict 1 2 2 true
printf 'Summaries: %s\n' "$out_dir"
if [[ -e "$out_dir/benchmark-failure" ]]; then
    echo 'benchmark_result=FAIL (overload threshold or terminal accounting breach; recovery still ran)' >&2
    exit 1
fi
