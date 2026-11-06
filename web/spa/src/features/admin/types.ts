// JSON DTOs mirror the snake_case payloads in internal/handlers/api_admin.go
// and internal/handlers/api_connections.go.
export interface AdminUser {
  id: string;
  username: string;
  active: boolean;
  is_platform_admin: boolean;
}

export interface AdminGroup {
  id: string;
  name: string;
  active: boolean;
}

export interface AdminDatabase {
  id: string;
  name: string;
  active: boolean;
}

export interface AdminMembership {
  user_id: string;
  group_id: string;
  username: string;
  group_name: string;
}

export interface AdminGrant {
  group_id: string;
  database_id: string;
  group_name: string;
  database_name: string;
  permission: string;
}

export interface AdminSnapshot {
  users: AdminUser[];
  groups: AdminGroup[];
  databases: AdminDatabase[];
  memberships: AdminMembership[];
  grants: AdminGrant[];
  selected_group?: AdminGroup;
}

export interface ConnectionRow {
  id: string;
  name: string;
  dbms: string;
  address: string;
  username: string;
  tls: string;
  active: boolean;
}

export interface ConnectionFormData {
  id?: string;
  name: string;
  dbms: string;
  host: string;
  port: string;
  database: string;
  username: string;
  tls: string;
  active: boolean;
  existing: boolean;
  sandbox: boolean;
}

export interface ConnectionCapability {
  name: string;
  supported: boolean;
}

export interface ConnectionTest {
  ran: boolean;
  ok: boolean;
  message: string;
}

export interface ConnectionDetail {
  form: ConnectionFormData;
  capabilities: ConnectionCapability[];
  receipt_sql?: string;
  test?: ConnectionTest;
}

export interface ViewStatus {
  message?: string;
  error?: string;
  pending?: boolean;
  refreshNeeded?: boolean;
  onRefresh?: () => void | Promise<unknown>;
}

export interface ConnectionList {
  connections: ConnectionRow[];
}

export type FormAction = (data: FormData) => void | Promise<void>;
