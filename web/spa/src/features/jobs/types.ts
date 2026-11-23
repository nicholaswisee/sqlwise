export type JobStatus = "queued" | "running" | "succeeded" | "failed" | "blocked";
export type JobEffect = "none" | "committed" | "partial" | "unknown";
export type RecoveryOperation = "resume" | "compensate";

export type JobView = {
  job_id: string;
  database_id: string;
  migration_id: string;
  request_id: string;
  execution_id: string;
  recovers_execution_id?: string;
  operation: string;
  status: JobStatus;
  effect: JobEffect;
  terminal: boolean;
  attempts: number;
  max_attempts: number;
  committed_batches: number;
  committed_rows: number;
  total_rows: number | null;
  error?: string;
  blocked_by?: string;
  resolved_by?: string;
  recovery: RecoveryOperation[];
  recovery_execution_id: string;
  next_run_at?: string;
  queued_at: string;
  started_at?: string;
  finished_at?: string;
  updated_at: string;
};

export type JobList = { jobs: JobView[] };
