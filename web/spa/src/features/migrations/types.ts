import type { WorkspaceDTO } from "../workspace/types";

export type Draft = {
  id: string;
  database_id: string;
  title: string;
  sql: string;
  sql_hash: string;
  dialect: string;
  revision: number;
  applied_version: number;
  last_status: string;
  created_by_name: string;
  created_at: string;
  updated_by_name: string;
  updated_at: string;
};
export type Attempt = {
  id: string;
  sequence: number;
  database_id: string;
  migration_id: string;
  migration_title: string;
  draft_revision: number;
  candidate_version: number;
  applied_version: number;
  sql: string;
  sql_hash: string;
  dialect: string;
  statement_count: number;
  executor_name: string;
  status: string;
  error?: string;
  failed_statement?: number;
  started_at: string;
  finished_at?: string | null;
  reconciled_at?: string | null;
};
export type History = {
  workspace: WorkspaceDTO;
  current_version: number;
  drafts: Draft[];
  attempts: Attempt[];
};
export type Detail = {
  workspace: WorkspaceDTO;
  current_version: number;
  draft: Draft;
  attempts: Attempt[];
  blocked: string;
  blocked_by?: Attempt | null;
};
export type Proposal = {
  schema: string;
  source_fingerprint: string;
  warnings: string[];
  sql: string;
  requires_review: boolean;
  changes: { text: string; destructive: boolean }[];
};
export type AttemptError = { attempt?: unknown };
