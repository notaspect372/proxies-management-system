"use client"

import * as React from "react"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Progress } from "@/components/ui/progress"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Separator } from "@/components/ui/separator"
import {
  Server,
  HardDrive,
  ChevronRight,
  Network,
  CheckCircle2,
  Clock,
  Activity,
  Globe,
  Boxes,
  Layers,
  Gauge,
  Loader2,
  RefreshCw,
  Flag,
  Trash2,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { api } from "@/lib/api"
import {
  InfrastructureMachine,
  InfrastructureCountryGroup,
  InfrastructureAssignment,
} from "@/lib/types"

// ─────────────────────────────────────────────────────────────────────────────
// UI helpers
// ─────────────────────────────────────────────────────────────────────────────

const statusColor: Record<string, string> = {
  online: "bg-emerald-500",
  active: "bg-emerald-500",
  degraded: "bg-amber-500",
  offline: "bg-red-500",
  failed: "bg-red-500",
  idle: "bg-zinc-400",
}

function StatusDot({ status, pulse = true }: { status: string; pulse?: boolean }) {
  return (
    <span className="relative inline-flex h-2.5 w-2.5">
      {pulse && status !== "offline" && status !== "idle" && status !== "failed" && (
        <span
          className={cn(
            "absolute inline-flex h-full w-full animate-ping rounded-full opacity-60",
            statusColor[status]
          )}
        />
      )}
      <span
        className={cn(
          "relative inline-flex h-2.5 w-2.5 rounded-full",
          statusColor[status] || "bg-zinc-400"
        )}
      />
    </span>
  )
}

function StatusBadge({ status }: { status: string }) {
  const labels: Record<string, string> = {
    active: "Healthy",
    online: "Healthy",
    failed: "Failed",
    offline: "Failed",
    idle: "Idle",
    degraded: "Degraded",
  }
  const label = labels[status] ?? status.charAt(0).toUpperCase() + status.slice(1)
  const variants: Record<string, string> = {
    active: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/20",
    idle: "bg-zinc-500/10 text-zinc-600 dark:text-zinc-400 border-zinc-500/20",
    failed: "bg-red-500/10 text-red-600 dark:text-red-400 border-red-500/20",
  }
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium",
        variants[status] || variants.idle
      )}
    >
      <StatusDot status={status} pulse={false} />
      {label}
    </span>
  )
}

function MachineIcon({ kind, className }: { kind: "main" | "mini"; className?: string }) {
  const Icon = kind === "main" ? Server : HardDrive
  return <Icon className={className} />
}

// Short pill labels + per-category tone so the four proxy types are easy to
// scan at a glance. Colors are kept distinct from the success/latency tones
// so the badge never gets confused with health state.
const PROXY_CATEGORY_LABELS: Record<string, string> = {
  static_residential: "Static res",
  rotating_residential: "Rot res",
  datacenter: "Datacenter",
  mobile: "Mobile",
}
const PROXY_CATEGORY_TONES: Record<string, string> = {
  static_residential:
    "bg-sky-500/10 text-sky-700 dark:text-sky-300 border-sky-500/30",
  rotating_residential:
    "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 border-emerald-500/30",
  datacenter:
    "bg-indigo-500/10 text-indigo-700 dark:text-indigo-300 border-indigo-500/30",
  mobile:
    "bg-orange-500/10 text-orange-700 dark:text-orange-300 border-orange-500/30",
}

function ProxyCategoryBadge({ category }: { category: string }) {
  const label = PROXY_CATEGORY_LABELS[category] ?? category
  const tone =
    PROXY_CATEGORY_TONES[category] ??
    "bg-muted text-muted-foreground border-border"
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-medium",
        tone
      )}
    >
      {label}
    </span>
  )
}

// Cost can be a tiny per-proxy figure (e.g. $0.0030) after dividing a $30
// batch across 10,000 proxies, OR a chunkier figure (e.g. $0.30 across
// 100). Show enough precision that small values aren't rendered as $0.00.
function formatCost(value: number): string {
  if (value >= 1) return value.toFixed(2)
  if (value >= 0.01) return value.toFixed(2)
  if (value >= 0.0001) return value.toFixed(4)
  return value.toFixed(6)
}

// A country group counts as "live" only if at least one of its proxies has
// served a request very recently — i.e. traffic is actually flowing right now,
// not just that the country was registered at some point.
const LIVE_WINDOW_MS = 2 * 60 * 1000
function isCountryLive(group: InfrastructureCountryGroup): boolean {
  const cutoff = Date.now() - LIVE_WINDOW_MS
  return (group.assignments ?? []).some((a) => {
    const t = Date.parse(a.last_used_at)
    return !Number.isNaN(t) && t >= cutoff
  })
}

function relativeTime(iso: string): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return "—"
  const diff = (Date.now() - t) / 1000
  if (diff < 5) return "just now"
  if (diff < 60) return `${Math.floor(diff)}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

// Map a VM/host id back to a friendly label for display in assignment rows.
function instanceLabel(machine: InfrastructureMachine, machineID: string): string {
  if (machineID === machine.id) return "Host OS"
  const vm = machine.vms.find((v) => v.id === machineID)
  return vm?.name ?? machineID
}

// ─────────────────────────────────────────────────────────────────────────────
// Page
// ─────────────────────────────────────────────────────────────────────────────

export default function InfrastructurePage() {
  const [machines, setMachines] = React.useState<InfrastructureMachine[] | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const [selectedMachineId, setSelectedMachineId] = React.useState<string | null>(null)
  const [selectedCountry, setSelectedCountry] = React.useState<string | null>(null)
  const [refreshing, setRefreshing] = React.useState(false)
  const [removingCountry, setRemovingCountry] = React.useState<string | null>(null)

  const load = React.useCallback(async () => {
    try {
      setRefreshing(true)
      const res = await api.getInfrastructure()
      setMachines(res.machines)
      setError(null)
      setSelectedMachineId((curr) => curr ?? res.machines[0]?.id ?? null)
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load infrastructure")
    } finally {
      setRefreshing(false)
    }
  }, [])

  React.useEffect(() => {
    void load()
    const t = setInterval(load, 10000)
    return () => clearInterval(t)
  }, [load])

  const selectedMachine =
    machines?.find((m) => m.id === selectedMachineId) ?? null

  // When the machine changes, reset/auto-pick the country.
  React.useEffect(() => {
    if (!selectedMachine) {
      setSelectedCountry(null)
      return
    }
    const groups = selectedMachine.country_groups ?? []
    if (groups.length === 0) {
      setSelectedCountry(null)
      return
    }
    setSelectedCountry((curr) => {
      if (curr && groups.some((g) => g.target_country === curr)) return curr
      return groups[0].target_country
    })
  }, [selectedMachine])

  const selectedGroup =
    selectedMachine?.country_groups?.find(
      (g) => g.target_country === selectedCountry
    ) ?? null

  const handleRemoveCountry = React.useCallback(
    async (group: InfrastructureCountryGroup) => {
      const assignmentCount = group.assignments?.length ?? 0
      const ok = window.confirm(
        `Release all ${assignmentCount} proxy assignment${
          assignmentCount === 1 ? "" : "s"
        } for ${group.target_country}? Scrapers will pick fresh proxies on the next checkout.`
      )
      if (!ok) return
      setRemovingCountry(group.target_country)
      try {
        await Promise.all(
          (group.assignments ?? []).map((a) =>
            api.releaseAssignment(a.machine_id, a.domain)
          )
        )
        if (selectedCountry === group.target_country) {
          setSelectedCountry(null)
        }
        await load()
      } catch (e) {
        setError(e instanceof Error ? e.message : "Failed to release assignments")
      } finally {
        setRemovingCountry(null)
      }
    },
    [load, selectedCountry]
  )

  // Aggregates for the overview strip
  const totals = React.useMemo(() => {
    if (!machines) {
      return { machines: 0, vms: 0, countries: 0, assignments: 0, avgSuccess: 0, avgLatency: 0 }
    }
    const allAssignments = machines.flatMap((m) =>
      (m.country_groups ?? []).flatMap((g) => g.assignments ?? [])
    )
    const countriesSet = new Set<string>()
    machines.forEach((m) =>
      (m.country_groups ?? []).forEach((g) => countriesSet.add(g.target_country))
    )
    const totalVMs = machines.reduce((s, m) => s + (m.vms?.length ?? 0), 0)
    const avgSuccess =
      allAssignments.reduce((s, a) => s + (a?.success_rate ?? 0), 0) /
      (allAssignments.length || 1)
    const avgLatency =
      allAssignments.reduce((s, a) => s + (a?.avg_response_time ?? 0), 0) /
      (allAssignments.length || 1)
    return {
      machines: machines.length,
      vms: totalVMs,
      countries: countriesSet.size,
      assignments: allAssignments.length,
      avgSuccess,
      avgLatency,
    }
  }, [machines])

  if (!machines && !error) {
    return (
      <div className="flex h-96 items-center justify-center">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Infrastructure</h1>
          <p className="text-muted-foreground">
            Live view of which proxies each machine is using, grouped by the country it&apos;s scraping.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={load}
            className="inline-flex items-center gap-1.5 rounded-md border bg-card px-3 py-1.5 text-xs text-muted-foreground hover:bg-accent disabled:opacity-50"
            disabled={refreshing}
          >
            <RefreshCw className={cn("h-3.5 w-3.5", refreshing && "animate-spin")} />
            {refreshing ? "Refreshing" : "Refresh"}
          </button>
          <div className="hidden items-center gap-2 rounded-full border bg-card/50 px-3 py-1.5 text-sm text-muted-foreground md:flex">
            <Boxes className="h-4 w-4" />
            <span className="text-foreground">{selectedMachine?.name ?? "—"}</span>
            <ChevronRight className="h-3.5 w-3.5 opacity-50" />
            <span className={selectedCountry ? "text-foreground" : ""}>
              {selectedCountry ?? "Pick a country"}
            </span>
          </div>
        </div>
      </div>

      {error && (
        <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          {error}
        </div>
      )}

      {/* Overview */}
      <div className="grid gap-4 md:grid-cols-4">
        <OverviewCard
          icon={<Server className="h-4 w-4" />}
          label="Machines"
          value={totals.machines.toString()}
          hint={`${totals.vms} VMs across the fleet`}
        />
        <OverviewCard
          icon={<Flag className="h-4 w-4" />}
          label="Countries Targeted"
          value={totals.countries.toString()}
          hint="Distinct ?country= values in flight"
        />
        <OverviewCard
          icon={<Network className="h-4 w-4" />}
          label="Active Assignments"
          value={totals.assignments.toString()}
          hint="Sticky proxy bindings"
        />
        <OverviewCard
          icon={<Gauge className="h-4 w-4" />}
          label="Avg Success / Latency"
          value={
            totals.assignments === 0 ? "—" : `${totals.avgSuccess.toFixed(1)}%`
          }
          hint={
            totals.assignments === 0
              ? "Waiting for traffic"
              : `${Math.round(totals.avgLatency)}ms avg`
          }
        />
      </div>

      {/* 3-column drill-down */}
      <div className="grid gap-4 lg:grid-cols-[minmax(280px,320px)_minmax(260px,320px)_1fr]">
        {/* Column 1: Machines */}
        <Card className="flex flex-col">
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2 text-base">
              <Boxes className="h-4 w-4 text-muted-foreground" />
              Machines
            </CardTitle>
            <CardDescription>Physical hosts in the fleet</CardDescription>
          </CardHeader>
          <Separator />
          <ScrollArea className="flex-1 min-h-[500px]">
            <div className="p-2">
              {(machines ?? []).map((m) => {
                const isSelected = m.id === selectedMachineId
                const groups = m.country_groups ?? []
                // Count only countries that are actively receiving requests
                // right now, so the number matches the filtered list in
                // column 2 (no more "4 countries" when only Russia is live).
                const liveCount = groups.filter(isCountryLive).length
                return (
                  <button
                    key={m.id}
                    onClick={() => {
                      setSelectedMachineId(m.id)
                      setSelectedCountry(null)
                    }}
                    className={cn(
                      "group mb-1 flex w-full items-center gap-3 rounded-lg border border-transparent px-3 py-2.5 text-left transition-all",
                      "hover:bg-accent hover:border-border",
                      isSelected && "bg-accent border-border shadow-sm"
                    )}
                  >
                    <div
                      className={cn(
                        "flex h-9 w-9 shrink-0 items-center justify-center rounded-md border",
                        isSelected
                          ? "bg-primary/10 border-primary/30 text-primary"
                          : "bg-muted/50 text-muted-foreground group-hover:text-foreground"
                      )}
                    >
                      <MachineIcon kind={m.kind} className="h-4 w-4" />
                    </div>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="truncate font-medium">{m.name}</span>
                      </div>
                      <div className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
                        <span className="truncate font-mono">{m.id}</span>
                        <span className="opacity-50">·</span>
                        <span className="shrink-0">
                          {liveCount} {liveCount === 1 ? "country" : "countries"}
                        </span>
                      </div>
                    </div>
                    <ChevronRight
                      className={cn(
                        "h-4 w-4 shrink-0 text-muted-foreground transition-transform",
                        isSelected && "translate-x-0.5 text-foreground"
                      )}
                    />
                  </button>
                )
              })}
            </div>
          </ScrollArea>
        </Card>

        {/* Column 2: Countries */}
        <Card className="flex flex-col">
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2 text-base">
              <Flag className="h-4 w-4 text-muted-foreground" />
              Target Countries
            </CardTitle>
            <CardDescription>
              {selectedMachine
                ? `Countries currently being scraped from ${selectedMachine.name}`
                : "Select a machine"}
            </CardDescription>
          </CardHeader>
          <Separator />
          <ScrollArea className="flex-1 min-h-[500px]">
            <div className="p-2">
              {(() => {
                // Only show countries with traffic flowing right now.
                // Stale country groups (last request older than the live
                // window) are hidden so the dashboard reflects what's
                // actually scraping, not historical state.
                const liveGroups = (selectedMachine?.country_groups ?? []).filter(
                  isCountryLive
                )
                if (!selectedMachine) {
                  return (
                    <EmptyCol
                      icon={<Flag className="h-8 w-8" />}
                      message="Pick a machine on the left"
                    />
                  )
                }
                if (liveGroups.length === 0) {
                  return (
                    <EmptyCol
                      icon={<Flag className="h-8 w-8" />}
                      message={`No countries are receiving requests from ${selectedMachine.name} right now.`}
                    />
                  )
                }
                return liveGroups.map((g) => {
                  const isSelected = g.target_country === selectedCountry
                  const isRemoving = removingCountry === g.target_country
                  const live = isCountryLive(g)
                  // Which VMs (or the host itself) are actually serving this
                  // country? Distinct machine_ids on the assignments tell us.
                  const vmLabels = Array.from(
                    new Set(
                      (g.assignments ?? []).map((a) =>
                        instanceLabel(selectedMachine, a.machine_id)
                      )
                    )
                  ).sort((a, b) => {
                    // Host OS last, VMs sorted by trailing number when present.
                    if (a === "Host OS") return 1
                    if (b === "Host OS") return -1
                    const na = parseInt(a.replace(/\D/g, ""), 10)
                    const nb = parseInt(b.replace(/\D/g, ""), 10)
                    if (Number.isFinite(na) && Number.isFinite(nb)) return na - nb
                    return a.localeCompare(b)
                  })
                  return (
                    <div
                      key={g.target_country}
                      className={cn(
                        "group mb-1 flex w-full items-center rounded-lg border border-transparent transition-all",
                        "hover:bg-accent hover:border-border",
                        isSelected && "bg-accent border-border shadow-sm"
                      )}
                    >
                      <button
                        type="button"
                        onClick={() => setSelectedCountry(g.target_country)}
                        className="flex flex-1 min-w-0 items-center gap-3 px-3 py-2.5 text-left"
                      >
                        <div
                          className={cn(
                            "flex h-9 w-9 shrink-0 items-center justify-center rounded-md border text-base",
                            isSelected
                              ? "bg-primary/10 border-primary/30 text-primary"
                              : "bg-muted/50 text-muted-foreground group-hover:text-foreground"
                          )}
                        >
                          <Flag className="h-4 w-4" />
                        </div>
                        <div className="min-w-0 flex-1">
                          <div className="flex min-w-0 items-center gap-2">
                            <span
                              className="shrink-0"
                              title={
                                live
                                  ? "Requests flowing in the last 2 minutes"
                                  : "No recent requests"
                              }
                            >
                              <StatusDot
                                status={live ? "active" : "idle"}
                                pulse={live}
                              />
                            </span>
                            <span className="min-w-0 truncate font-medium">
                              {g.target_country}
                            </span>
                            <Badge
                              variant="secondary"
                              className="h-5 shrink-0 px-1.5 text-[10px] uppercase tracking-wide"
                            >
                              {g.active_count}/{g.total_count} healthy
                            </Badge>
                          </div>
                          <div className="mt-0.5 truncate text-xs text-muted-foreground">
                            {g.total_count} {g.total_count === 1 ? "proxy" : "proxies"}{" "}
                            {live ? "in use" : "in used"}
                          </div>
                          {vmLabels.length > 0 && (
                            <div className="mt-1 flex flex-wrap items-center gap-1">
                              {vmLabels.slice(0, 4).map((label) => (
                                <span
                                  key={label}
                                  title={`Served from ${label}`}
                                  className={cn(
                                    "inline-flex items-center gap-1 rounded-full border px-1.5 py-0.5 text-[10px] font-medium",
                                    label === "Host OS"
                                      ? "border-border bg-muted/60 text-muted-foreground"
                                      : "border-primary/20 bg-primary/10 text-primary"
                                  )}
                                >
                                  <Layers className="h-2.5 w-2.5" />
                                  {label}
                                </span>
                              ))}
                              {vmLabels.length > 4 && (
                                <span className="inline-flex items-center rounded-full border bg-muted/60 px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
                                  +{vmLabels.length - 4}
                                </span>
                              )}
                            </div>
                          )}
                        </div>
                      </button>
                      <button
                        type="button"
                        onClick={(e) => {
                          e.stopPropagation()
                          void handleRemoveCountry(g)
                        }}
                        disabled={isRemoving}
                        title={`Release all assignments for ${g.target_country}`}
                        aria-label={`Remove ${g.target_country}`}
                        className={cn(
                          "ml-1 mr-1 flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border/40 bg-muted/40 text-foreground/70 transition-colors",
                          "hover:bg-destructive/10 hover:text-destructive hover:border-destructive/40",
                          "disabled:opacity-50 disabled:cursor-not-allowed"
                        )}
                      >
                        {isRemoving ? (
                          <Loader2 className="h-4 w-4 animate-spin" />
                        ) : (
                          <Trash2 className="h-4 w-4" />
                        )}
                      </button>
                      <ChevronRight
                        className={cn(
                          "mr-3 h-4 w-4 shrink-0 text-muted-foreground transition-transform",
                          isSelected && "translate-x-0.5 text-foreground"
                        )}
                      />
                    </div>
                  )
                })
              })()}
            </div>
          </ScrollArea>
        </Card>

        {/* Column 3: Proxies */}
        <Card className="flex flex-col">
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2 text-base">
              <Network className="h-4 w-4 text-muted-foreground" />
              Proxies in Use
            </CardTitle>
            <CardDescription>
              {selectedGroup
                ? `${selectedGroup.total_count} proxies currently scraping ${selectedGroup.target_country}`
                : "Select a country to see its proxies"}
            </CardDescription>
          </CardHeader>
          <Separator />
          <CardContent className="flex-1 p-4">
            {selectedGroup && selectedMachine ? (
              <ProxyList machine={selectedMachine} group={selectedGroup} />
            ) : (
              <EmptyCol
                icon={<Network className="h-10 w-10" />}
                message="Drill down on a country to see the proxies behind it"
              />
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

// ─────────────────────────────────────────────────────────────────────────────
// Sub-components
// ─────────────────────────────────────────────────────────────────────────────

function OverviewCard({
  icon,
  label,
  value,
  hint,
}: {
  icon: React.ReactNode
  label: string
  value: string
  hint: string
}) {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium">{label}</CardTitle>
        <div className="text-muted-foreground">{icon}</div>
      </CardHeader>
      <CardContent>
        <div className="text-2xl font-bold">{value}</div>
        <p className="text-xs text-muted-foreground">{hint}</p>
      </CardContent>
    </Card>
  )
}

function EmptyCol({
  icon,
  message,
}: {
  icon: React.ReactNode
  message: string
}) {
  return (
    <div className="flex h-[400px] flex-col items-center justify-center gap-3 px-4 text-center text-muted-foreground">
      <div className="opacity-40">{icon}</div>
      <p className="text-sm">{message}</p>
    </div>
  )
}

function ProxyList({
  machine,
  group,
}: {
  machine: InfrastructureMachine
  group: InfrastructureCountryGroup
}) {
  const sorted = [...(group.assignments ?? [])].sort(
    (a, b) => Date.parse(b.last_used_at) - Date.parse(a.last_used_at)
  )
  const avgSuccess =
    sorted.reduce((s, a) => s + a.success_rate, 0) / (sorted.length || 1)
  const avgLatency =
    sorted.reduce((s, a) => s + a.avg_response_time, 0) / (sorted.length || 1)

  // Bucket assignments by the worker that owns them (host or VM id). The
  // user picks a single VM at a time via the tab strip so they can focus
  // on one worker's behaviour for this country.
  const lanes = React.useMemo(() => {
    const map = new Map<string, InfrastructureAssignment[]>()
    for (const a of sorted) {
      const list = map.get(a.machine_id) ?? []
      list.push(a)
      map.set(a.machine_id, list)
    }
    return Array.from(map.entries())
      .map(([machineId, items]) => {
        const proxyIds = new Set(items.map((i) => i.proxy_id))
        const laneSuccess =
          items.reduce((s, a) => s + a.success_rate, 0) / items.length
        const laneLatency =
          items.reduce((s, a) => s + a.avg_response_time, 0) / items.length
        return {
          machineId,
          label: instanceLabel(machine, machineId),
          items,
          proxyCount: proxyIds.size,
          successAvg: laneSuccess,
          latencyAvg: laneLatency,
        }
      })
      .sort((a, b) => {
        // Pin "Host OS" first; otherwise natural-numeric sort by label.
        if (a.machineId === machine.id) return -1
        if (b.machineId === machine.id) return 1
        return a.label.localeCompare(b.label, undefined, { numeric: true })
      })
  }, [sorted, machine])

  const [selectedVM, setSelectedVM] = React.useState<string | null>(null)
  // Reset / auto-pick the VM when the country changes or the set of lanes
  // shifts (e.g. a VM stops scraping this country).
  React.useEffect(() => {
    if (lanes.length === 0) {
      setSelectedVM(null)
      return
    }
    setSelectedVM((curr) => {
      if (curr && lanes.some((l) => l.machineId === curr)) return curr
      return lanes[0].machineId
    })
  }, [lanes])

  if (sorted.length === 0) {
    return (
      <EmptyCol
        icon={<Network className="h-10 w-10" />}
        message="No assignments for this country yet."
      />
    )
  }

  const activeLane = lanes.find((l) => l.machineId === selectedVM) ?? lanes[0]

  return (
    <div className="flex h-full flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border bg-muted/30 px-4 py-3">
        <div className="flex items-center gap-2 text-xs uppercase tracking-wide text-muted-foreground">
          <span>{machine.name}</span>
          <ChevronRight className="h-3 w-3" />
          <span className="text-foreground font-semibold">
            {group.target_country}
          </span>
        </div>
        <div className="flex items-center gap-4 text-sm">
          <SummaryStat
            label="Healthy"
            value={`${group.active_count}/${group.total_count}`}
          />
          <SummaryStat label="Avg Success" value={`${avgSuccess.toFixed(1)}%`} />
          <SummaryStat label="Avg Latency" value={`${Math.round(avgLatency)}ms`} />
        </div>
      </div>

      {/* VM tab strip — one tab per worker (host + each VM) that is
          actually scraping this country. */}
      <div className="flex flex-wrap items-center gap-1 border-b">
        {lanes.map((lane) => {
          const isActive = lane.machineId === activeLane?.machineId
          return (
            <button
              key={lane.machineId}
              onClick={() => setSelectedVM(lane.machineId)}
              className={cn(
                "flex items-center gap-2 border-b-2 px-3 py-2 text-sm transition-colors",
                isActive
                  ? "border-primary text-foreground"
                  : "border-transparent text-muted-foreground hover:text-foreground"
              )}
            >
              <Layers className="h-3.5 w-3.5" />
              <span className="font-medium">{lane.label}</span>
              <span className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] tabular-nums text-muted-foreground">
                {lane.proxyCount}
              </span>
            </button>
          )
        })}
      </div>

      {activeLane && (
        <>
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border/60 bg-muted/20 px-3 py-2">
            <div className="flex items-center gap-2 text-sm">
              <Layers className="h-3.5 w-3.5 text-muted-foreground" />
              <span className="font-semibold">{activeLane.label}</span>
              <span className="text-xs text-muted-foreground">
                · {activeLane.proxyCount}{" "}
                {activeLane.proxyCount === 1 ? "proxy" : "proxies"}
              </span>
            </div>
            <div className="flex items-center gap-4 text-xs">
              <span
                className={cn(
                  "tabular-nums font-medium",
                  laneSuccessTone(activeLane.successAvg)
                )}
              >
                {activeLane.successAvg.toFixed(1)}% success
              </span>
              <span
                className={cn(
                  "tabular-nums font-medium",
                  laneLatencyTone(activeLane.latencyAvg)
                )}
              >
                {Math.round(activeLane.latencyAvg)}ms avg
              </span>
            </div>
          </div>

          <ScrollArea className="flex-1">
            <div className="space-y-2 pr-2">
              {activeLane.items.map((a) => (
                <AssignmentRow
                  key={`${a.machine_id}-${a.domain}`}
                  assignment={a}
                />
              ))}
            </div>
          </ScrollArea>
        </>
      )}
    </div>
  )
}

function laneSuccessTone(s: number): string {
  if (s >= 95) return "text-emerald-600 dark:text-emerald-400"
  if (s >= 80) return "text-amber-600 dark:text-amber-400"
  return "text-red-600 dark:text-red-400"
}

function laneLatencyTone(ms: number): string {
  if (ms === 0) return "text-muted-foreground"
  if (ms < 200) return "text-emerald-600 dark:text-emerald-400"
  if (ms < 500) return "text-amber-600 dark:text-amber-400"
  return "text-red-600 dark:text-red-400"
}


function SummaryStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col items-end">
      <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
      <span className="font-semibold tabular-nums">{value}</span>
    </div>
  )
}

function AssignmentRow({
  assignment,
}: {
  assignment: InfrastructureAssignment
}) {
  const successTone =
    assignment.success_rate >= 95
      ? "text-emerald-600 dark:text-emerald-400"
      : assignment.success_rate >= 80
        ? "text-amber-600 dark:text-amber-400"
        : "text-red-600 dark:text-red-400"

  const latencyTone =
    assignment.avg_response_time === 0
      ? "text-muted-foreground"
      : assignment.avg_response_time < 200
        ? "text-emerald-600 dark:text-emerald-400"
        : assignment.avg_response_time < 500
          ? "text-amber-600 dark:text-amber-400"
          : "text-red-600 dark:text-red-400"

  return (
    <div className="rounded-lg border bg-card p-3 transition-colors hover:border-foreground/20">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm font-semibold truncate">
              {assignment.domain}
            </span>
            <Badge
              variant="outline"
              className="font-mono text-xs uppercase tracking-wide px-2 py-0.5"
            >
              {assignment.proxy_protocol}
            </Badge>
            {assignment.proxy_category && (
              <ProxyCategoryBadge category={assignment.proxy_category} />
            )}
            {typeof assignment.proxy_cost === "number" && assignment.proxy_cost > 0 && (
              <span className="text-sm font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">
                ${formatCost(assignment.proxy_cost)}
              </span>
            )}
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
            <span className="font-mono">{assignment.proxy_address}</span>
            {assignment.proxy_country && (
              <span className="flex items-center gap-1">
                <Globe className="h-3 w-3" />
                proxy: {assignment.proxy_country}
              </span>
            )}
            <span className="flex items-center gap-1">
              <Clock className="h-3 w-3" />
              {relativeTime(assignment.last_used_at)}
            </span>
            <span>{assignment.request_count.toLocaleString("en-US")} reqs</span>
          </div>
        </div>
        <StatusBadge status={assignment.proxy_status} />
      </div>

      <div className="mt-3 grid grid-cols-3 gap-3 text-xs">
        <div>
          <div className="flex items-center gap-1 text-muted-foreground">
            <CheckCircle2 className="h-3 w-3" />
            Success
          </div>
          <div className={cn("font-semibold tabular-nums", successTone)}>
            {assignment.success_rate.toFixed(1)}%
          </div>
          <Progress value={assignment.success_rate} className="mt-1 h-1" />
        </div>
        <div>
          <div className="flex items-center gap-1 text-muted-foreground">
            <Clock className="h-3 w-3" />
            Latency
          </div>
          <div className={cn("font-semibold tabular-nums", latencyTone)}>
            {assignment.avg_response_time === 0
              ? "—"
              : `${assignment.avg_response_time}ms`}
          </div>
        </div>
        <div>
          <div className="flex items-center gap-1 text-muted-foreground">
            <Activity className="h-3 w-3" />
            Assigned
          </div>
          <div className="font-semibold tabular-nums">
            {relativeTime(assignment.assigned_at)}
          </div>
        </div>
      </div>
    </div>
  )
}
