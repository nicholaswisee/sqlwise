export type PlanNode = {
  type: string;
  operation?: string;
  relation?: string;
  index?: string;
  join?: string;
  startup_cost: number;
  total_cost: number;
  plan_rows: number;
  actual_time?: number;
  actual_rows?: number;
  loops?: number;
  children: PlanNode[];
};
export type QueryPlan = {
  dialect: string;
  mode: string;
  sql_hash: string;
  warnings: string[];
  planning_time?: number;
  execution_time?: number;
  root: PlanNode;
};
export type ExplainRequest = { sql: string; mode: "explain" | "analyze"; confirmed: boolean };
