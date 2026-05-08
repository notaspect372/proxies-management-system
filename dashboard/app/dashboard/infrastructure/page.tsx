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
  const label = status.charAt(0).toUpperCase() + status.slice(1)
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
      const ok = window.confirm(
        `Release all ${group.total_count} proxy assignment${
          group.total_count === 1 ? "" : "s"
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
                        <StatusDot status={groups.length ? "online" : "idle"} />
                        <span className="truncate font-medium">{m.name}</span>
                      </div>
                      <div className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
                        <span className="truncate font-mono">{m.id}</span>
                        <span className="opacity-50">·</span>
                        <span className="shrink-0">
                          {groups.length} {groups.length === 1 ? "country" : "countries"}
                        </span>
                        <span className="opacity-50">·</span>
                        <span className="shrink-0">
                          {m.total_assignments} live
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
              {selectedMachine?.country_groups?.length ? (
                selectedMachine.country_groups.map((g) => {
                  const isSelected = g.target_country === selectedCountry
                  const isRemoving = removingCountry === g.target_country
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
                          <div className="flex items-center gap-2">
                            <span className="truncate font-medium">{g.target_country}</span>
                            <Badge
                              variant="secondary"
                              className="h-5 px-1.5 text-[10px] uppercase tracking-wide"
                            >
                              {g.active_count}/{g.total_count} active
                            </Badge>
                          </div>
                          <div className="mt-0.5 text-xs text-muted-foreground">
                            {g.total_count} {g.total_count === 1 ? "proxy" : "proxies"} in use
                          </div>
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
                          "mr-1 flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors",
                          "hover:bg-destructive/10 hover:text-destructive",
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
              ) : (
                <EmptyCol
                  icon={<Flag className="h-8 w-8" />}
                  message={
                    selectedMachine
                      ? `No scraper has called /api/v1/proxy?machine_id=${selectedMachine.id}... yet.`
                      : "Pick a machine on the left"
                  }
                />
              )}
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

  if (sorted.length === 0) {
    return (
      <EmptyCol
        icon={<Network className="h-10 w-10" />}
        message="No assignments for this country yet."
      />
    )
  }

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
            label="Active"
            value={`${group.active_count}/${group.total_count}`}
          />
          <SummaryStat label="Avg Success" value={`${avgSuccess.toFixed(1)}%`} />
          <SummaryStat label="Avg Latency" value={`${Math.round(avgLatency)}ms`} />
        </div>
      </div>

      <ScrollArea className="flex-1">
        <div className="space-y-2 pr-2">
          {sorted.map((a) => (
            <AssignmentRow
              key={`${a.machine_id}-${a.domain}`}
              assignment={a}
              instance={instanceLabel(machine, a.machine_id)}
            />
          ))}
        </div>
      </ScrollArea>
    </div>
  )
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
  instance,
}: {
  assignment: InfrastructureAssignment
  instance: string
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
          <div className="flex items-center gap-2">
            <StatusDot status={assignment.proxy_status} />
            <span className="font-mono text-sm font-semibold truncate">
              {assignment.domain}
            </span>
            <Badge
              variant="outline"
              className="font-mono text-[10px] uppercase tracking-wide"
            >
              {assignment.proxy_protocol}
            </Badge>
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
              <Layers className="h-3 w-3" />
              {instance}
            </span>
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
