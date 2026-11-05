import type { WorkspaceMetadata } from "../../components/workspace";

export type WorkspaceDTO = WorkspaceMetadata & {
  dialect?: string;
  capabilities: {
    connection_test: boolean;
    schema_inspection: boolean;
    data_preview: boolean;
    migration_execution: boolean;
    atomic_migrations: boolean;
    schema_diff: boolean;
    explain: boolean;
    explain_analyze: boolean;
  };
  current_version: number;
  version_track: WorkspaceMetadata["version_track"];
  track_earlier: number;
};
