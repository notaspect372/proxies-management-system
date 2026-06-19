// API Response Types

export type ProxyCategory =
  | "static_residential"
  | "rotating_residential"
  | "datacenter"
  | "mobile"

export const PROXY_CATEGORIES: {
  value: ProxyCategory
  label: string
  description: string
}[] = [
  {
    value: "static_residential",
    label: "Static Residential",
    description: "Sticky IPs from real ISP networks",
  },
  {
    value: "rotating_residential",
    label: "Rotating Residential",
    description: "Residential IPs rotated per request or session",
  },
  {
    value: "datacenter",
    label: "Datacenter",
    description: "Fast datacenter IPs (cheaper, easier to detect)",
  },
  {
    value: "mobile",
    label: "Mobile",
    description: "Mobile carrier IPs (4G/5G), highest trust",
  },
]

export interface Proxy {
  id: number
  address: string
  protocol: "http" | "https" | "socks4" | "socks4a" | "socks5"
  status: "active" | "failed" | "idle"
  category?: ProxyCategory
  cost?: number
  country?: string
  requests: number
  success_rate: number
  avg_response_time: number
  last_check: string
  username?: string
  created_at: string
  updated_at: string
}

export interface ProxiesResponse {
  proxies: Proxy[]
  pagination: {
    page: number
    limit: number
    total: number
    total_pages: number
  }
}

export interface DashboardStats {
  active_proxies: number
  total_proxies: number
  total_requests: number
  avg_success_rate: number
  avg_response_time: number
  request_growth: number
  success_rate_growth: number
  response_time_delta: number
}

export interface ChartDataPoint {
  time: string
  value?: number
  success?: number
  failure?: number
}

export interface ChartResponse {
  data: ChartDataPoint[]
}

export interface LogEntry {
  id: string
  timestamp: string
  level: "info" | "warning" | "error" | "success"
  message: string
  details?: string
  metadata?: Record<string, any>
}

export interface LogsResponse {
  logs: LogEntry[]
  pagination: {
    page: number
    limit: number
    total: number
    total_pages: number
  }
}

export interface SystemMetrics {
  memory: {
    total: number
    used: number
    available: number
    percentage: number
  }
  cpu: {
    percentage: number
    cores: number
  }
  disk: {
    total: number
    used: number
    free: number
    percentage: number
  }
  runtime: {
    goroutines: number
    threads: number
    gc_pause_count: number
    mem_alloc: number
    mem_sys: number
  }
}

export interface Settings {
  authentication: {
    enabled: boolean
    username: string
    password: string
  }
  rotation: {
    method: "random" | "roundrobin" | "least_conn" | "time_based"
    time_based?: {
      interval: number
    }
    remove_unhealthy: boolean
    fallback: boolean
    fallback_max_retries: number
    follow_redirect: boolean
    timeout: number
    retries: number
    allowed_protocols: string[]
    max_response_time: number
    min_success_rate: number
  }
  rate_limit: {
    enabled: boolean
    interval: number
    max_requests: number
  }
  healthcheck: {
    timeout: number
    workers: number
    url: string
    status: number
    headers: string[]
  }
  log_retention: {
    enabled: boolean
    retention_days: number
    compression_after_days: number
    cleanup_interval_hours: number
  }
}

export interface AuthResponse {
  token: string
  user: {
    username: string
  }
}

export interface ApiError {
  error: string
  details?: string
}

// Request Types
export interface AddProxyRequest {
  address: string
  protocol: "http" | "https" | "socks4" | "socks4a" | "socks5"
  username?: string
  password?: string
  category?: ProxyCategory
  cost?: number
  country?: string
}

export interface UpdateProxyRequest {
  address?: string
  protocol?: "http" | "https" | "socks4" | "socks4a" | "socks5"
  username?: string
  password?: string
  category?: ProxyCategory
  cost?: number
  country?: string
}

// Infrastructure / checkout
export interface InfrastructureAssignment {
  machine_id: string          // host or VM id
  domain: string
  target_country: string
  assigned_at: string
  last_used_at: string
  request_count: number
  proxy_id: number
  proxy_address: string
  proxy_protocol: string
  proxy_username?: string
  proxy_status: "active" | "failed" | "idle"
  proxy_category?: ProxyCategory
  proxy_country?: string
  proxy_cost?: number
  success_rate: number
  avg_response_time: number
}

export interface InfrastructureCountryGroup {
  target_country: string
  active_count: number
  total_count: number
  assignments: InfrastructureAssignment[]
}

export interface FleetVM {
  id: string
  name: string
}

export interface InfrastructureMachine {
  id: string
  name: string
  hostname: string
  kind: "main" | "mini"
  vms: FleetVM[]
  country_groups: InfrastructureCountryGroup[]
  total_assignments: number
}

export interface InfrastructureResponse {
  machines: InfrastructureMachine[]
}

// Cooldown / Recovery Test view — one row per (proxy, machine, domain) scope
// currently in the banned state.
export interface CooldownRow {
  proxy_id: number
  proxy_address: string
  target_domain: string
  target_country?: string
  machine_id: string
  state: "active" | "banned"
  display_state: "active" | "cooldown" | "recovery_test"
  banned_at?: string
  next_probe_at?: string
  cooldown_remaining_sec: number
  probe_attempt: number
  successful_since_recovery: number
  last_probe_at?: string
  last_failure_at?: string
}

export interface CooldownResponse {
  cooldowns: CooldownRow[]
  total: number
}

export interface BulkProxyRequest {
  proxies: AddProxyRequest[]
}

export interface BulkDeleteRequest {
  ids: number[]
}

export interface ProxyTestResult {
  id: number
  address: string
  status: "active" | "failed"
  response_time?: number
  error?: string
  tested_at: string
  duration?: number // Alias for response_time for better clarity
}

// Aux listener CRUD — entries persisted in AUX_LISTENERS_SHEET. Manual
// entries (AUX_LISTENERS) come back as `manual` so the UI can show them
// read-only and keep the user from picking a conflicting port.

export interface ListenerEntry {
  machine_id: string
  country: string
  port: number
}

export interface ListenerState {
  entries: ListenerEntry[]
  manual: ListenerEntry[]
  env_path: string
  fleet_machines: string[]
}
