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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Loader2, Timer, RefreshCw } from "lucide-react"
import { api } from "@/lib/api"
import { CooldownRow } from "@/lib/types"
import { cn } from "@/lib/utils"

// Format a remaining-seconds number into a human "1h 20m" or "23s" string.
function formatRemaining(sec: number): string {
  if (sec <= 0) return "—"
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

function stateBadge(displayState: string) {
  switch (displayState) {
    case "cooldown":
      return (
        <Badge className="bg-red-500/10 text-red-600 dark:text-red-400 border border-red-500/20">
          <span className="mr-1.5 inline-block h-2 w-2 rounded-full bg-red-500" />
          Banned
        </Badge>
      )
    case "recovery_test":
      return (
        <Badge className="bg-amber-500/10 text-amber-600 dark:text-amber-400 border border-amber-500/20">
          <span className="mr-1.5 inline-block h-2 w-2 rounded-full bg-amber-500" />
          Recovery Test
        </Badge>
      )
    default:
      return (
        <Badge className="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border border-emerald-500/20">
          <span className="mr-1.5 inline-block h-2 w-2 rounded-full bg-emerald-500" />
          Active
        </Badge>
      )
  }
}

export default function CooldownPage() {
  const [rows, setRows] = React.useState<CooldownRow[] | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const [refreshing, setRefreshing] = React.useState(false)

  const [search, setSearch] = React.useState("")
  const [machineFilter, setMachineFilter] = React.useState<string>("all")
  const [stateFilter, setStateFilter] = React.useState<string>("all")

  const load = React.useCallback(async () => {
    try {
      setRefreshing(true)
      const res = await api.getCooldowns()
      setRows(res.cooldowns)
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load cooldowns")
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
      if (machineFilter !== "all" && r.machine_id !== machineFilter) return false
      if (stateFilter !== "all" && r.display_state !== stateFilter) return false
      if (search.trim()) {
        const needle = search.toLowerCase()
        const hay =
          `${r.proxy_address} ${r.target_domain} ${r.target_country ?? ""} ${r.machine_id}`.toLowerCase()
        if (!hay.includes(needle)) return false
      }
      return true
    })
  }, [rows, search, machineFilter, stateFilter])

  const cooldownCount = filtered.filter((r) => r.display_state === "cooldown").length
  const recoveryCount = filtered.filter((r) => r.display_state === "recovery_test").length

  return (
    <div className="flex flex-col gap-6 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-semibold">
            <Timer className="h-6 w-6" />
            Cooldown & Recovery
          </h1>
          <p className="text-sm text-muted-foreground">
            Per-(proxy, machine, site) bans. Banned scopes are auto-probed; if a real
            request to the site succeeds, the proxy returns to Active for that site.
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
            <CardDescription>In cooldown (no probe yet)</CardDescription>
            <CardTitle className="text-3xl text-red-600 dark:text-red-400">
              {cooldownCount}
            </CardTitle>
          </CardHeader>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardDescription>In recovery test</CardDescription>
            <CardTitle className="text-3xl text-amber-600 dark:text-amber-400">
              {recoveryCount}
            </CardTitle>
          </CardHeader>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center gap-3">
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
            <Select value={stateFilter} onValueChange={setStateFilter}>
              <SelectTrigger className="w-[180px]">
                <SelectValue placeholder="State" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All states</SelectItem>
                <SelectItem value="cooldown">Banned (cooldown)</SelectItem>
                <SelectItem value="recovery_test">Recovery Test</SelectItem>
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
                  <TableHead>Proxy IP</TableHead>
                  <TableHead>Site</TableHead>
                  <TableHead>Country</TableHead>
                  <TableHead>Machine</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Cooldown Remaining</TableHead>
                  <TableHead>Trial Requests</TableHead>
                  <TableHead>Successes Since Recovery</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows === null ? (
                  <TableRow>
                    <TableCell colSpan={8} className="text-center text-muted-foreground py-8">
                      <Loader2 className="mx-auto h-5 w-5 animate-spin" />
                    </TableCell>
                  </TableRow>
                ) : filtered.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={8} className="text-center text-muted-foreground py-8">
                      {rows.length === 0
                        ? "No proxies are in cooldown right now — every active scope is healthy."
                        : "No rows match the current filters."}
                    </TableCell>
                  </TableRow>
                ) : (
                  filtered.map((r) => (
                    <TableRow key={`${r.proxy_id}-${r.machine_id}-${r.target_domain}`}>
                      <TableCell className="font-mono text-xs">{r.proxy_address}</TableCell>
                      <TableCell className="font-mono text-xs">{r.target_domain}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {r.target_country || "—"}
                      </TableCell>
                      <TableCell className="font-mono text-xs">{r.machine_id}</TableCell>
                      <TableCell>{stateBadge(r.display_state)}</TableCell>
                      <TableCell className="tabular-nums">
                        {formatRemaining(r.cooldown_remaining_sec)}
                      </TableCell>
                      <TableCell className="tabular-nums">
                        {r.probe_attempt === 0
                          ? "0 trial requests sent"
                          : `${r.probe_attempt} trial request${r.probe_attempt === 1 ? "" : "s"} sent`}
                      </TableCell>
                      <TableCell className="tabular-nums">
                        {r.successful_since_recovery}
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
