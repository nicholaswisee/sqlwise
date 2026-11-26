import http from "k6/http";
import exec from "k6/execution";
import { Counter, Rate, Trend } from "k6/metrics";
import { sleep } from "k6";

const fixtures = JSON.parse(open(__ENV.FIXTURES));
const accepted = new Counter("accepted_202");
const terminalSucceeded = new Counter("terminal_succeeded");
const terminalFailed = new Counter("terminal_failed");
const recoveryBlocked = new Counter("recovery_blocked");
const timedOut = new Counter("poll_timeout");
const outstandingJobs = new Counter("outstanding_jobs");
const expectedStaleFailures = new Counter("expected_terminal_stale_conflicts");
const unexpectedJobStates = new Counter("unexpected_job_states");
const unexpectedErrorRate = new Rate("unexpected_http_error_rate");
const expectedErrors = new Counter("expected_http_errors");
const unexpectedErrors = new Counter("unexpected_http_errors");
const authorizedReads = new Counter("authorized_reads");
const completionSeconds = new Trend("job_completion_seconds", true);
const queueWaitSeconds = new Trend("queue_wait_seconds", true);
const executionSeconds = new Trend("worker_execution_seconds", true);
const waveDrainSeconds = new Trend("wave_drain_seconds", true);
const requestTimeouts = new Counter("request_timeouts");
const submissionTimeouts = new Counter("submission_timeouts");
const noJobAfterTimeout = new Counter("no_job_after_submission_timeout");
const acceptedObserved = new Counter("accepted_jobs_observed");
const reconciliationReads = new Counter("reconciliation_reads");
const transportErrors = new Counter("transport_errors");

export const options = {
  scenarios: {
    workload: {
      executor: "shared-iterations",
      vus: Number(__ENV.VUS),
      iterations: fixtures.length,
      maxDuration: __ENV.MAX_DURATION || "20m",
    },
  },
  summaryTrendStats: ["count", "avg", "med", "p(90)", "p(95)", "max"],
  thresholds: {
    http_req_duration: ["p(95)<500"],
    unexpected_http_error_rate: ["rate<0.01"],
  },
};

function request(method, path, payload, csrf, session) {
  const options = { headers: { Accept: "application/json" }, timeout: __ENV.HTTP_TIMEOUT || "60s" };
  if (csrf) options.headers["X-CSRF-Token"] = csrf;
  if (session) options.headers.Cookie = session.cookie;
  if (payload !== undefined) options.headers["Content-Type"] = "application/json";
  const response = http.request(
    method,
    `${__ENV.BASE_URL}/api/v1${path}`,
    payload === undefined ? null : JSON.stringify(payload),
    options,
  );
  if (response.status > 0 && response.status < 400) unexpectedErrorRate.add(false);
  if (response.status === 0) requestTimeouts.add(1);
  return response;
}
function error(status, phase) {
  unexpectedErrorRate.add(true);
  if (status === 0) {
    transportErrors.add(1);
    console.error(`unexpected_transport_failure phase=${phase}`);
  } else {
    unexpectedErrors.add(1, { status: String(status) });
    console.error(`unexpected_http_status=${status} phase=${phase}`);
  }
}

export function setup() {
  const jar = http.cookieJar();
  const sessions = [];
  for (let i = 0; i < Number(__ENV.VUS); i++) {
    const challenge = request("GET", "/auth/login-challenge");
    if (challenge.status !== 200) throw new Error(`challenge HTTP ${challenge.status}`);
    const login = request(
      "POST",
      "/auth/login",
      { username: __ENV.USERNAME, password: __ENV.PASSWORD },
      challenge.json("data.csrf_token"),
    );
    if (login.status !== 200) throw new Error(`login HTTP ${login.status}`);
    const cookie = login.cookies.sqlwise_session?.[0]?.value;
    const csrf = login.json("data.csrf_token");
    if (!cookie || !csrf) throw new Error("login response missing session fields");
    sessions.push({ cookie: `sqlwise_session=${cookie}`, csrf });
    jar.clear(__ENV.BASE_URL);
  }
  return { sessions, startedAt: Date.now() };
}
export default function (stageData) {
  const session = stageData.sessions[exec.vu.idInTest - 1];
  if (!session) {
    unexpectedJobStates.add(1);
    outstandingJobs.add(1);
    return;
  }
  const fixture = fixtures[exec.scenario.iterationInTest];
  const read = request(
    "GET",
    `/databases/${encodeURIComponent(fixture.database_id)}/jobs?limit=1`,
    undefined,
    undefined,
    session,
  );
  if (read.status === 200) authorizedReads.add(1);
  else error(read.status, "authorized_read");

  const started = Date.now();
  const response = request(
    "POST",
    fixture.execute_path.replace("/api/v1", ""),
    {
      request_id: fixture.request_id,
      idempotency_key: fixture.idempotency_key,
    },
    session.csrf,
    session,
  );
  let jobID;
  if (response.status === 409 && __ENV.EXPECT_CONFLICT === "true") {
    const code = response.json("error.code");
    const message = response.json("error.message") || "";
    if (
      code === "conflict" &&
      /database (?:is at version \d+, not \d+|moved from version \d+ to \d+)/i.test(message)
    ) {
      expectedErrors.add(1);
      unexpectedErrorRate.add(false);
    } else error(response.status, "unexpected_conflict");
    return;
  }
  if (response.status === 202) {
    accepted.add(1);
    acceptedObserved.add(1);
    jobID = response.json("data.job_id");
  } else if (response.status === 0) {
    submissionTimeouts.add(1);
    error(0, "execute");
    for (let attempt = 0; attempt < 2 && !jobID; attempt++) {
      if (attempt) sleep(0.25);
      const jobs = request(
        "GET",
        `/databases/${encodeURIComponent(fixture.database_id)}/jobs?limit=100`,
        undefined,
        undefined,
        session,
      );
      if (jobs.status !== 200) {
        error(jobs.status, "submission_reconciliation");
        outstandingJobs.add(1);
        return;
      }
      reconciliationReads.add(1);
      const found = (jobs.json("data.jobs") || []).find(
        (job) => job.request_id === fixture.request_id,
      );
      if (found) jobID = found.job_id;
    }
    if (!jobID) {
      noJobAfterTimeout.add(1);
      return;
    }
    acceptedObserved.add(1);
  } else {
    error(response.status, "execute");
    return;
  }
  if (!jobID) {
    unexpectedJobStates.add(1);
    timedOut.add(1);
    outstandingJobs.add(1);
    return;
  }

  const deadline = Date.now() + Number(__ENV.POLL_TIMEOUT_SECONDS || 180) * 1000;
  while (Date.now() < deadline) {
    const poll = request(
      "GET",
      `/databases/${encodeURIComponent(fixture.database_id)}/jobs/${encodeURIComponent(jobID)}`,
      undefined,
      undefined,
      session,
    );
    if (poll.status !== 200) {
      error(poll.status, "poll");
      sleep(0.25);
      continue;
    }
    const job = poll.json("data");
    if (job.terminal) {
      completionSeconds.add((Date.now() - started) / 1000);
      waveDrainSeconds.add((Date.now() - stageData.startedAt) / 1000);
      const queued = Date.parse(job.queued_at);
      const startedAt = Date.parse(job.started_at);
      const finishedAt = Date.parse(job.finished_at);
      if (Number.isFinite(queued) && Number.isFinite(startedAt))
        queueWaitSeconds.add(Math.max(0, startedAt - queued) / 1000);
      if (Number.isFinite(startedAt) && Number.isFinite(finishedAt))
        executionSeconds.add(Math.max(0, finishedAt - startedAt) / 1000);
      if (job.status === "succeeded") terminalSucceeded.add(1);
      else if (job.status === "failed") {
        terminalFailed.add(1);
        if (
          __ENV.EXPECT_CONFLICT === "true" &&
          job.effect === "none" &&
          /database (?:is at version \d+, not \d+|moved from version \d+ to \d+)/i.test(
            job.error || "",
          )
        )
          expectedStaleFailures.add(1);
      } else if (job.status === "blocked") recoveryBlocked.add(1, { effect: job.effect });
      else unexpectedJobStates.add(1);
      return;
    }
    sleep(0.25);
  }
  timedOut.add(1);
  outstandingJobs.add(1);
}

export function handleSummary(data) {
  return { [__ENV.OUT]: JSON.stringify(data, null, 2) };
}
