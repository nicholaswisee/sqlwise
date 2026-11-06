import type { WorkspaceDTO } from "../workspace/types";
export type SchemaColumn = {
  name: string;
  type: string;
  nullable: boolean;
  default?: string | null;
  primary_key: boolean;
};
export type SchemaDefinition = { name: string; kind: string; definition: string };
export type SchemaTable = {
  id: string;
  name: string;
  columns: SchemaColumn[];
  constraints: SchemaDefinition[];
  indexes: SchemaDefinition[];
};
export type SchemaExplorerDTO = {
  workspace: WorkspaceDTO;
  schemas: string[];
  schema: string;
  tables: SchemaTable[];
};
export type TableCell = { text: string; null: boolean; binary: boolean; truncated: boolean };
export type TableDataDTO = {
  workspace: WorkspaceDTO;
  schema: string;
  table: string;
  offset: number;
  limit: number;
  has_next: boolean;
  ordered: boolean;
  truncated: boolean;
  columns: { name: string; type: string }[];
  rows: TableCell[][];
};
