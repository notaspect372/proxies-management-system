import {
  Proxy,
  ProxiesResponse,
  DashboardStats,
  ChartResponse,
  LogsResponse,
  SystemMetrics,
  Settings,
  AuthResponse,
  AddProxyRequest,
  UpdateProxyRequest,
  BulkProxyRequest,
  BulkDeleteRequest,
  ProxyTestResult,
  InfrastructureResponse,
  CooldownResponse,
  ListenerEntry,
  ListenerState,
} from "./types"

function stripTrailingSlash(url: string): string {
  return url.replace(/\/$/, "")
}

class ApiClient {
  private token: string | null = null
  /** If set, always use this base (e.g. tests). */
  private readonly baseUrlOverride: string | null

  constructor(baseUrl?: string) {
    this.baseUrlOverride =
      baseUrl !== undefined ? stripTrailingSlash(baseUrl) : null
    if (typeof window !== "undefined") {
      this.token = localStorage.getItem("auth_token")
    }
  }

  /**
   * Dev (`next dev`): browser calls the Go API directly on :8001 (avoids Next proxy noise; needs CORS on the API).
   * Production build: same-origin `/api/v1/...` → Next rewrites to the core (Docker / `next start`).
   * Override anytime with NEXT_PUBLIC_API_URL.
   */
  private getBaseUrl(): string {
    if (this.baseUrlOverride !== null) {
      return this.baseUrlOverride
    }
    const env = process.env.NEXT_PUBLIC_API_URL?.trim()
    if (env) {
      return stripTrailingSlash(env)
    }
    if (typeof window !== "undefined") {
      return process.env.NODE_ENV === "development"
        ? "http://127.0.0.1:8001"
        : ""
    }
    return "http://127.0.0.1:8001"
  }

  /** WebSockets are not proxied by Next dev; talk to the API port directly when using same-origin HTTP. */
  private getWebSocketBase(): string {
    const base = this.getBaseUrl()
    if (base !== "") {
      if (base.startsWith("https")) {
        return "wss" + base.slice("https".length)
      }
      return base.replace(/^http/, "ws")
    }
    if (typeof window !== "undefined") {
      const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
      const { hostname } = window.location
      return `${proto}//${hostname}:8001`
    }
    return "ws://127.0.0.1:8001"
  }

  setToken(token: string) {
    this.token = token
    if (typeof window !== "undefined") {
      localStorage.setItem("auth_token", token)
    }
  }

  clearToken() {
    this.token = null
    if (typeof window !== "undefined") {
      localStorage.removeItem("auth_token")
    }
  }

  private getHeaders(): HeadersInit {
    const headers: HeadersInit = {
      "Content-Type": "application/json",
    }
    if (this.token) {
      headers["Authorization"] = `Bearer ${this.token}`
    }
    return headers
  }

  private networkErrorMessage(): string {
    const base = this.getBaseUrl()
    const hint = base
      ? ` (${base})`
      : " (via this app’s /api/v1 proxy to port 8001)"
    return `Cannot reach the API${hint}. From dashboard folder run npm run dev (starts Docker + API), or npm run dev:ui after starting the core on port 8001.`
  }

  private async fetchWithNetworkGuard(
    url: string,
    init?: RequestInit
  ): Promise<Response> {
    try {
      return await fetch(url, init)
    } catch (e) {
      if (e instanceof TypeError) {
        throw new Error(this.networkErrorMessage())
      }
      throw e
    }
  }

  private async request<T>(
    endpoint: string,
    options: RequestInit = {}
  ): Promise<T> {
    const base = this.getBaseUrl()
    const url = `${base}${endpoint}`
    const response = await this.fetchWithNetworkGuard(url, {
      ...options,
      headers: {
        ...this.getHeaders(),
        ...options.headers,
      },
    })

    if (!response.ok) {
      const error = await response.json().catch(() => ({
        error: `HTTP ${response.status}: ${response.statusText}`,
      }))
      throw new Error(error.error || error.message || "Request failed")
    }

    if (response.status === 204) {
      return {} as T
    }

    return response.json()
  }

  async login(username: string, password: string): Promise<AuthResponse> {
    const response = await this.request<AuthResponse>("/api/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    })
    this.setToken(response.token)
    return response
  }

  async getDashboardStats(): Promise<DashboardStats> {
    return this.request<DashboardStats>("/api/v1/dashboard/stats")
  }

  async getResponseTimeChart(interval: string = "4h"): Promise<ChartResponse> {
    return this.request<ChartResponse>(
      `/api/v1/dashboard/charts/response-time?interval=${interval}`
    )
  }

  async getSuccessRateChart(interval: string = "4h"): Promise<ChartResponse> {
    return this.request<ChartResponse>(
      `/api/v1/dashboard/charts/success-rate?interval=${interval}`
    )
  }

  async getProxies(params?: {
    page?: number
    limit?: number
    search?: string
    status?: string
    protocol?: string
    category?: string
    country?: string
    sort?: string
    order?: "asc" | "desc"
  }): Promise<ProxiesResponse> {
    const searchParams = new URLSearchParams()
    if (params) {
      Object.entries(params).forEach(([key, value]) => {
        if (value !== undefined) {
          searchParams.append(key, value.toString())
        }
      })
    }
    const query = searchParams.toString()
    return this.request<ProxiesResponse>(
      `/api/v1/proxies${query ? `?${query}` : ""}`
    )
  }

  async addProxy(proxy: AddProxyRequest): Promise<Proxy> {
    return this.request<Proxy>("/api/v1/proxies", {
      method: "POST",
      body: JSON.stringify(proxy),
    })
  }

  async updateProxy(id: number, proxy: UpdateProxyRequest): Promise<Proxy> {
    return this.request<Proxy>(`/api/v1/proxies/${id}`, {
      method: "PUT",
      body: JSON.stringify(proxy),
    })
  }

  async deleteProxy(id: number): Promise<void> {
    return this.request<void>(`/api/v1/proxies/${id}`, {
      method: "DELETE",
    })
  }

  async bulkAddProxies(request: BulkProxyRequest): Promise<{
    created: number
    failed: number
    results: Array<{ address: string; status: string; id?: string }>
  }> {
    return this.request("/api/v1/proxies/bulk", {
      method: "POST",
      body: JSON.stringify(request),
    })
  }

  async bulkDeleteProxies(request: BulkDeleteRequest): Promise<{
    deleted: number
    message: string
  }> {
    return this.request("/api/v1/proxies/bulk-delete", {
      method: "POST",
      body: JSON.stringify(request),
    })
  }

  async testProxy(id: number): Promise<ProxyTestResult> {
    return this.request<ProxyTestResult>(`/api/v1/proxies/${id}/test`, {
      method: "POST",
    })
  }

  async exportProxies(
    format: "txt" | "json" | "csv" = "txt",
    status?: string
  ): Promise<Blob> {
    const params = new URLSearchParams({ format })
    if (status) params.append("status", status)

    const base = this.getBaseUrl()
    const url = `${base}/api/v1/proxies/export?${params.toString()}`
    const response = await this.fetchWithNetworkGuard(url, {
      headers: this.getHeaders(),
    })

    if (!response.ok) {
      throw new Error("Export failed")
    }

    return response.blob()
  }

  async reloadProxies(): Promise<{ status: string; message: string }> {
    return this.request("/api/v1/proxies/reload", {
      method: "POST",
    })
  }

  async detectCountries(force: boolean = false): Promise<{
    scanned: number
    updated: number
    force: boolean
  }> {
    return this.request(
      `/api/v1/proxies/detect-countries${force ? "?force=true" : ""}`,
      { method: "POST" }
    )
  }

  async getLogs(params?: {
    page?: number
    limit?: number
    level?: string
    search?: string
    source?: string
    start_time?: string
    end_time?: string
  }): Promise<LogsResponse> {
    const searchParams = new URLSearchParams()
    if (params) {
      Object.entries(params).forEach(([key, value]) => {
        if (value !== undefined) {
          searchParams.append(key, value.toString())
        }
      })
    }
    const query = searchParams.toString()
    return this.request<LogsResponse>(`/api/v1/logs${query ? `?${query}` : ""}`)
  }

  async exportLogs(
    format: "txt" | "json" = "txt",
    params?: {
      level?: string
      source?: string
      start_time?: string
      end_time?: string
    }
  ): Promise<Blob> {
    const searchParams = new URLSearchParams({ format })
    if (params) {
      Object.entries(params).forEach(([key, value]) => {
        if (value !== undefined) {
          searchParams.append(key, value.toString())
        }
      })
    }

    const base = this.getBaseUrl()
    const url = `${base}/api/v1/logs/export?${searchParams.toString()}`
    const response = await this.fetchWithNetworkGuard(url, {
      headers: this.getHeaders(),
    })

    if (!response.ok) {
      throw new Error("Export failed")
    }

    return response.blob()
  }

  async getSystemMetrics(): Promise<SystemMetrics> {
    return this.request<SystemMetrics>("/api/v1/metrics/system")
  }

  async getInfrastructure(): Promise<InfrastructureResponse> {
    return this.request<InfrastructureResponse>("/api/v1/infrastructure")
  }

  async releaseAssignment(machineId: string, domain: string): Promise<void> {
    const params = new URLSearchParams({ machine_id: machineId, domain })
    return this.request<void>(`/api/v1/proxy?${params.toString()}`, {
      method: "DELETE",
    })
  }

  async getCooldowns(): Promise<CooldownResponse> {
    return this.request<CooldownResponse>("/api/v1/cooldowns")
  }

  async getListeners(): Promise<ListenerState> {
    return this.request<ListenerState>("/api/v1/admin/listeners")
  }

  async addListener(entry: ListenerEntry): Promise<ListenerState> {
    return this.request<ListenerState>("/api/v1/admin/listeners", {
      method: "POST",
      body: JSON.stringify(entry),
    })
  }

  async deleteListener(port: number): Promise<ListenerState> {
    return this.request<ListenerState>(`/api/v1/admin/listeners/${port}`, {
      method: "DELETE",
    })
  }

  async getSettings(): Promise<Settings> {
    return this.request<Settings>("/api/v1/settings")
  }

  async updateSettings(settings: Partial<Settings>): Promise<{
    message: string
    config: Settings
  }> {
    return this.request("/api/v1/settings", {
      method: "PUT",
      body: JSON.stringify(settings),
    })
  }

  async resetSettings(): Promise<{
    message: string
    config: Settings
  }> {
    return this.request("/api/v1/settings/reset", {
      method: "POST",
    })
  }

  createDashboardWebSocket(
    onMessage: (data: DashboardStats) => void
  ): WebSocket {
    const wsBase = this.getWebSocketBase()
    const ws = new WebSocket(
      `${wsBase}/ws/dashboard${this.token ? `?token=${this.token}` : ""}`
    )

    ws.onmessage = (event) => {
      const data = JSON.parse(event.data)
      if (data.type === "stats_update") {
        onMessage(data.data)
      }
    }

    return ws
  }

  createLogsWebSocket(
    onMessage: (log: any) => void,
    levels?: string[],
    source?: string
  ): WebSocket {
    const wsBase = this.getWebSocketBase()
    const ws = new WebSocket(
      `${wsBase}/ws/logs${this.token ? `?token=${this.token}` : ""}`
    )

    ws.onopen = () => {
      if ((levels && levels.length > 0) || source) {
        ws.send(
          JSON.stringify({
            action: "filter",
            levels: levels || [],
            source: source || "",
          })
        )
      }
    }

    ws.onmessage = (event) => {
      const log = JSON.parse(event.data)
      onMessage(log)
    }

    return ws
  }
}

export const api = new ApiClient()

export { ApiClient }
