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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Loader2, Ban, RefreshCw, ChevronRight, ChevronDown } from "lucide-react"
import { api } from "@/lib/api"
import { BanRow, BanClassification, RecoveryTrialRow } from "@/lib/types"
import { cn } from "@/lib/utils"

// A stable key per (proxy, machine, domain) scope — used for row identity and
// to cache fetched trial history.
function scopeKey(r: { proxy_id: number; machine_id: string; target_domain: string }) {
  return `${r.proxy_id}-${r.machine_id}-${r.target_domain}`
}

// Format an ISO timestamp into a compact local date+time, or "—" when absent.
function formatDateTime(iso?: string): string {
  if (!iso) return "—"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  })
}

function classificationBadge(classification: BanClassification) {
  switch (classification) {
    case "permanent":
      return (
        <Badge className="bg-red-500/10 text-red-600 dark:text-red-400 border border-red-500/20">
          <span className="mr-1.5 inline-block h-2 w-2 rounded-full bg-red-500" />
          Permanent
        </Badge>
      )
    case "recovering":
      return (
        <Badge className="bg-amber-500/10 text-amber-600 dark:text-amber-400 border border-amber-500/20">
          <span className="mr-1.5 inline-block h-2 w-2 rounded-full bg-amber-500" />
          Recovering
        </Badge>
      )
    default:
      return (
        <Badge className="bg-muted text-muted-foreground border">
          <span className="mr-1.5 inline-block h-2 w-2 rounded-full bg-muted-foreground" />
          Cooldown
        </Badge>
      )
  }
}

function resultBadge(result: "pass" | "fail") {
  if (result === "pass") {
    return (
      <Badge className="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border border-emerald-500/20">
        Pass
      </Badge>
    )
  }
  return (
    <Badge className="bg-red-500/10 text-red-600 dark:text-red-400 border border-red-500/20">
      Fail
    </Badge>
  )
}

// Detail panel showing a scope's recovery-trial history. Fetched lazily when a
// row is expanded; this is the debug view that proves the IP was tried and
// whether it worked.
function TrialHistory({ row }: { row: BanRow }) {
  const [trials, setTrials] = React.useState<RecoveryTrialRow[] | null>(null)
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    let active = true
    void (async () => {
      try {
        const res = await api.getRecoveryTrials({
          proxy_id: row.proxy_id,
          machine_id: row.machine_id,
          target_domain: row.target_domain,
          limit: 100,
        })
        if (active) {
          setTrials(res.trials)
          setError(null)
        }
      } catch (e) {
        if (active) {
          setError(e instanceof Error ? e.message : "Failed to load trial history")
        }
      }
    })()
    return () => {
      active = false
    }
  }, [row.proxy_id, row.machine_id, row.target_domain])

  if (error) {
    return (
      <div className="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-600 dark:text-red-400">
        {error}
      </div>
    )
  }

  if (trials === null) {
    return (
      <div className="flex items-center gap-2 px-2 py-4 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        Loading recovery-trial history…
      </div>
    )
  }

  if (trials.length === 0) {
    return (
      <div className="px-2 py-4 text-sm text-muted-foreground">
        No recovery trials recorded yet for this scope. Trials are written each
        time the banned proxy is routed a real request for this site.
      </div>
    )
  }

  return (
    <div className="rounded-md border bg-muted/30 p-3">
      <p className="mb-2 text-xs font-medium text-muted-foreground">
        Recovery-trial history ({trials.length}) — newest first
      </p>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>When</TableHead>
            <TableHead>Proxy IP tried</TableHead>
            <TableHead>Result</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Response</TableHead>
            <TableHead>Reason</TableHead>
            <TableHead>Trial #</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {trials.map((t, i) => (
            <TableRow key={`${t.attempted_at}-${i}`}>
              <TableCell className="whitespace-nowrap text-xs">
                {formatDateTime(t.attempted_at)}
              </TableCell>
              <TableCell className="font-mono text-xs">{t.proxy_address}</TableCell>
              <TableCell>{resultBadge(t.result)}</TableCell>
              <TableCell className="tabular-nums text-xs">
                {t.status_code ?? "—"}
              </TableCell>
              <TableCell className="tabular-nums text-xs">
                {t.response_time_ms != null ? `${t.response_time_ms} ms` : "—"}
              </TableCell>
              <TableCell className="max-w-xs truncate text-xs text-muted-foreground" title={t.reason ?? ""}>
                {t.reason || "—"}
              </TableCell>
              <TableCell className="tabular-nums text-xs">
                {t.probe_attempt_after}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

export default function BansPage() {
  const [rows, setRows] = React.useState<BanRow[] | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const [refreshing, setRefreshing] = React.useState(false)

  const [permanentOnly, setPermanentOnly] = React.useState(true)
  const [search, setSearch] = React.useState("")
  const [machineFilter, setMachineFilter] = React.useState<string>("all")
  const [expanded, setExpanded] = React.useState<string | null>(null)

  const load = React.useCallback(async () => {
    try {
      setRefreshing(true)
      // Fetch all bans (classification computed server-side) and filter
      // client-side so toggling Permanent-only doesn't require a refetch.
      const res = await api.getBans()
      setRows(res.bans)
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load bans")
    } finally {
      setRefreshing(false)
    }
  }, [])

  React.useEffect(() => {
    void load()
    const t = setInterval(load, 15000)
    return () => clearInterval(t)
  }, [load])

  const machineOptions = React.useMemo(() => {
    if (!rows) return []
    const set = new Set<string>()
    rows.forEach((r) => set.add(r.machine_id))
    return Array.from(set).sort()
  }, [rows])

  const filtered = React.useMemo(() => {
    if (!rows) return []
    return rows.filter((r) => {
      if (permanentOnly && r.classification !== "permanent") return false
      if (machineFilter !== "all" && r.machine_id !== machineFilter) return false
      if (search.trim()) {
        const needle = search.toLowerCase()
        const hay =
          `${r.proxy_address} ${r.target_domain} ${r.target_country ?? ""} ${r.machine_id}`.toLowerCase()
        if (!hay.includes(needle)) return false
      }
      return true
    })
  }, [rows, permanentOnly, search, machineFilter])

  const permanentCount = rows?.filter((r) => r.classification === "permanent").length ?? 0
  const recoveringCount = rows?.filter((r) => r.classification === "recovering").length ?? 0

  return (
    <div className="flex flex-col gap-6 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-semibold">
            <Ban className="h-6 w-6" />
            Ban Analysis
          </h1>
          <p className="text-sm text-muted-foreground">
            Permanently banned scopes (proxy + site + machine) that never recover,
            plus the per-scope recovery-trial history. Click a row to see whether
            the proxy IP was actually tried and if it worked.
          </p>
        </div>
        <button
          onClick={() => void load()}
          disabled={refreshing}
          className={cn(
            "inline-flex items-center gap-2 rounded-md border bg-card px-3 py-1.5 text-sm font-medium transition-colors hover:bg-accent",
            refreshing && "opacity-60"
          )}
        >
          {refreshing ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <RefreshCw className="h-4 w-4" />
          )}
          Refresh
        </button>
      </div>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
        <Card>
          <CardHeader className="pb-2">
            <CardDescription>Total banned scopes</CardDescription>
            <CardTitle className="text-3xl">{rows?.length ?? "—"}</CardTitle>
          </CardHeader>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardDescription>Permanent (never recover)</CardDescription>
            <CardTitle className="text-3xl text-red-600 dark:text-red-400">
              {permanentCount}
            </CardTitle>
          </CardHeader>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardDescription>Recovering (still trialed)</CardDescription>
            <CardTitle className="text-3xl text-amber-600 dark:text-amber-400">
              {recoveringCount}
            </CardTitle>
          </CardHeader>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center gap-4">
            <div className="flex items-center gap-2">
              <Switch
                id="permanent-only"
                checked={permanentOnly}
                onCheckedChange={setPermanentOnly}
              />
              <Label htmlFor="permanent-only" className="cursor-pointer text-sm">
                Permanent only
              </Label>
            </div>
            <Input
              placeholder="Search proxy IP, site, country, machine..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="max-w-sm"
            />
            <Select value={machineFilter} onValueChange={setMachineFilter}>
              <SelectTrigger className="w-[180px]">
                <SelectValue placeholder="Machine" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All machines</SelectItem>
                {machineOptions.map((m) => (
                  <SelectItem key={m} value={m}>
                    {m}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </CardHeader>
        <CardContent>
          {error && (
            <div className="mb-4 rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-600 dark:text-red-400">
              {error}
            </div>
          )}
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-8" />
                  <TableHead>Proxy IP</TableHead>
                  <TableHead>Site</TableHead>
                  <TableHead>Country</TableHead>
                  <TableHead>Machine</TableHead>
                  <TableHead>Classification</TableHead>
                  <TableHead>Banned since</TableHead>
                  <TableHead>Failed trials</TableHead>
                  <TableHead>Next trial at</TableHead>
                  <TableHead>Last trial result</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows === null ? (
                  <TableRow>
                    <TableCell colSpan={10} className="text-center text-muted-foreground py-8">
                      <Loader2 className="mx-auto h-5 w-5 animate-spin" />
                    </TableCell>
                  </TableRow>
                ) : filtered.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={10} className="text-center text-muted-foreground py-8">
                      {rows.length === 0
                        ? "No banned scopes right now."
                        : permanentOnly
                          ? "No permanently banned scopes — every ban is still recovering or in cooldown."
                          : "No rows match the current filters."}
                    </TableCell>
                  </TableRow>
                ) : (
                  filtered.map((r) => {
                    const key = scopeKey(r)
                    const isOpen = expanded === key
                    return (
                      <React.Fragment key={key}>
                        <TableRow
                          className="cursor-pointer"
                          onClick={() => setExpanded(isOpen ? null : key)}
                        >
                          <TableCell>
                            {isOpen ? (
                              <ChevronDown className="h-4 w-4 text-muted-foreground" />
                            ) : (
                              <ChevronRight className="h-4 w-4 text-muted-foreground" />
                            )}
                          </TableCell>
                          <TableCell className="font-mono text-xs">{r.proxy_address}</TableCell>
                          <TableCell className="font-mono text-xs">{r.target_domain}</TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            {r.target_country || "—"}
                          </TableCell>
                          <TableCell className="font-mono text-xs">{r.machine_id}</TableCell>
                          <TableCell>{classificationBadge(r.classification)}</TableCell>
                          <TableCell className="whitespace-nowrap text-xs">
                            {formatDateTime(r.banned_at)}
                          </TableCell>
                          <TableCell className="tabular-nums">{r.probe_attempt}</TableCell>
                          <TableCell className="whitespace-nowrap text-xs">
                            {formatDateTime(r.next_probe_at)}
                          </TableCell>
                          <TableCell>
                            {r.probe_attempt > 0 ? (
                              resultBadge("fail")
                            ) : (
                              <span className="text-xs text-muted-foreground">No trial yet</span>
                            )}
                          </TableCell>
                        </TableRow>
                        {isOpen && (
                          <TableRow>
                            <TableCell colSpan={10} className="bg-muted/20 p-3">
                              <TrialHistory row={r} />
                            </TableCell>
                          </TableRow>
                        )}
                      </React.Fragment>
                    )
                  })
                )}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
