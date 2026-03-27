export interface ProviderStatus {
  openai: string;
  anthropic: string;
  ollama: string;
}

export interface HealthResponse {
  status: string;
  version: string;
  uptime_seconds: number;
  storage_backend?: string;
  providers: ProviderStatus;
}

export interface Budget {
  agent: string;
  period: string;
  limit_usd: number;
  spent_usd: number;
  remaining_usd: number;
  percentage_used: number;
  status: string;
  action_on_exceed: string;
  period_resets_at: string;
}

export interface GlobalBudget {
  period: string;
  limit_usd: number;
  spent_usd: number;
  remaining_usd: number;
}

export interface BudgetsResponse {
  budgets: Budget[];
  global: GlobalBudget;
}

export interface CostBreakdown {
  agent: string;
  model: string;
  bucket?: string;
  requests: number;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
}

export interface CostsResponse {
  total_usd: number;
  total_requests: number;
  total_input_tokens: number;
  total_output_tokens: number;
  breakdown: CostBreakdown[];
}

export interface ModelPricing {
  model: string;
  provider: string;
  input_per_million: number;
  output_per_million: number;
}

export interface PricingResponse {
  pricing: ModelPricing[];
}

export interface Agent {
  name: string;
  status: string;
  total_requests: number;
  total_cost_usd: number;
  last_seen_at: string;
  budget_status: string;
}

export interface AgentsResponse {
  agents: Agent[];
}

export interface Alert {
  id?: string;
  type: string;
  agent: string;
  message: string;
  severity: string;
  timestamp?: string;
  threshold_pct?: number;
  spent_usd?: number;
  limit_usd?: number;
  action?: string;
  data?: Record<string, unknown>;
}

export interface CostRecord {
  id?: string;
  agent: string;
  model: string;
  provider: string;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
  timestamp?: string;
  trace_id?: string;
  task_id?: string;
  downgraded?: boolean;
  original_model?: string;
}

export interface APIError {
  code: string;
  message: string;
  agent?: string;
  budget_limit_usd?: number;
  budget_spent_usd?: number;
}

export interface APIErrorResponse {
  error: APIError;
}

export interface CostUpdateEvent {
  agent: string;
  spent_usd: number;
  request_cost_usd: number;
}

export interface BudgetAlertEvent {
  agent: string;
  threshold_pct: number;
  spent_usd: number;
  limit_usd: number;
}

export interface AgentKilledEvent {
  agent: string;
  reason: string;
}
