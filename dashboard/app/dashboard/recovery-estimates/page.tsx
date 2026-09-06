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
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Loader2, Brain, RefreshCw, ChevronRight } from "lucide-react"
import { api } from "@/lib/api"
import {
  DomainRecoveryEstimate,
  RecoveryEstimateDetail,
} from "@/lib/types"
import { cn } from "@/lib/utils"

// Render a second count the way an operator reads it: "1h 30m", "45m", "2d 4h".
function formatDuration(sec: number): string {
  if (!sec || sec <= 0) return "—"
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return h > 0 ? `${d}d ${h}h` : `${d}d`
  if (h > 0) return m > 0 ? `${h}h ${m}m` : `${h}h`
  if (m > 0) return `${m}m`
  return `${sec}s`
}

// The headline sentence the page exists to produce.
function estimateSentence(e: DomainRecoveryEstimate): string {
  const where = e.target_country ? `${e.target_country} site` : "site"
  return `Estimated cooldown period for ${where} ${e.target_domain} is ${formatDuration(
    e.estimated_sec
  )}`
}

function formatWhen(iso?: string): string {
  if (!iso) return "—"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  return d.toLocaleString()
}

// Confidence is purely a function of how many recoveries we have actually
// watched. Kept deliberately coarse — it is a reading aid, not a statistic.
function confidence(samples: number): {
  label: string
  className: string
} {
  if (samples >= 10)
    return {
      label: "High",
      className:
        "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border border-emerald-500/20",
    }
  if (samples >= 3)
    return {
      label: "Medium",
      className:
        "bg-amber-500/10 text-amber-600 dark:text-amber-400 border border-amber-500/20",
    }
  return {
    label: "Low",
    className:
      "bg-muted text-muted-foreground border border-border",
  }
}

export default function RecoveryEstimatesPage() {
  const [rows, setRows] = React.useState<DomainRecoveryEstimate[] | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const [refreshing, setRefreshing] = React.useState(false)
  const [search, setSearch] = React.useState("")

  const [openDomain, setOpenDomain] = React.useState<string | null>(null)
  const [detail, setDetail] = React.useState<RecoveryEstimateDetail | null>(null)
  const [detailLoading, setDetailLoading] = React.useState(false)
  const [detailError, setDetailError] = React.useState<string | null>(null)

  const load = React.useCallback(async () => {
    try {
      setRefreshing(true)
      const res = await api.getRecoveryEstimates()
      setRows(res.estimates)
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load estimates")
    } finally {
      setRefreshing(false)
    }
  }, [])

  React.useEffect(() => {
    void load()
    const t = setInterval(load, 60000)
    return () => clearInterval(t)
  }, [load])

  // Drill-down: everything linked to one site's estimate.
  const openDetail = React.useCallback(async (domain: string) => {
    setOpenDomain(domain)
    setDetail(null)
    setDetailError(null)
    setDetailLoading(true)
    try {
      setDetail(await api.getRecoveryEstimateDetail(domain))
    } catch (e) {
      setDetailError(e instanceof Error ? e.message : "Failed to load detail")
    } finally {
      setDetailLoading(false)
    }
  }, [])

  const filtered = React.useMemo(() => {
    if (!rows) return []
    const needle = search.trim().toLowerCase()
    if (!needle) return rows
    return rows.filter((r) =>
      `${r.target_domain} ${r.target_country ?? ""}`
        .toLowerCase()
        .includes(needle)
    )
  }, [rows, search])

  const totalSamples = (rows ?? []).reduce((n, r) => n + r.samples, 0)
  const fastest = (rows ?? []).reduce<DomainRecoveryEstimate | null>(
    (best, r) => (best === null || r.min_sec < best.min_sec ? r : best),
    null
  )

  const selected = rows?.find((r) => r.target_domain === openDomain) ?? null

  return (
    <div className="flex flex-col gap-6 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-semibold">
            <Brain className="h-6 w-6" />
            Recovery Estimates
          </h1>
          <p className="text-sm text-muted-foreground">
            How long bans actually last, learned per site from every observed
            recovery across all machines. Click a row to see the proxies and
            logs behind the number.
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
            <CardDescription>Sites with an estimate</CardDescription>
            <CardTitle className="text-3xl">{rows?.length ?? "—"}</CardTitle>
          </CardHeader>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardDescription>Recoveries observed</CardDescription>
            <CardTitle className="text-3xl">
              {rows === null ? "—" : totalSamples}
            </CardTitle>
          </CardHeader>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardDescription>Fastest observed unban</CardDescription>
            <CardTitle className="text-3xl text-emerald-600 dark:text-emerald-400">
              {fastest ? formatDuration(fastest.min_sec) : "—"}
            </CardTitle>
            {fastest && (
              <p className="truncate font-mono text-xs text-muted-foreground">
                {fastest.target_domain}
              </p>
            )}
          </CardHeader>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <Input
            placeholder="Search site or country..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="max-w-sm"
          />
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
                  <TableHead>Site</TableHead>
                  <TableHead>Country</TableHead>
                  <TableHead>Estimated cooldown</TableHead>
                  <TableHead>Median</TableHead>
                  <TableHead>Fastest</TableHead>
                  <TableHead>Slowest</TableHead>
                  <TableHead>Avg trials</TableHead>
                  <TableHead>Observations</TableHead>
                  <TableHead>Confidence</TableHead>
                  <TableHead>Banned now</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows === null ? (
                  <TableRow>
                    <TableCell
                      colSpan={11}
                      className="py-8 text-center text-muted-foreground"
                    >
                      <Loader2 className="mx-auto h-5 w-5 animate-spin" />
                    </TableCell>
                  </TableRow>
                ) : filtered.length === 0 ? (
                  <TableRow>
                    <TableCell
                      colSpan={11}
                      className="py-8 text-center text-muted-foreground"
                    >
                      {rows.length === 0
                        ? "Nothing learned yet. An estimate appears once a banned proxy has been seen recovering — the first observations land after the trial ladder completes."
                        : "No sites match the search."}
                    </TableCell>
                  </TableRow>
                ) : (
                  filtered.map((r) => {
                    const c = confidence(r.samples)
                    return (
                      <TableRow
                        key={r.target_domain}
                        onClick={() => void openDetail(r.target_domain)}
                        className="cursor-pointer"
                      >
                        <TableCell className="font-mono text-xs">
                          {r.target_domain}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {r.target_country || "—"}
                        </TableCell>
                        <TableCell className="font-semibold tabular-nums">
                          {formatDuration(r.estimated_sec)}
                        </TableCell>
                        <TableCell className="tabular-nums">
                          {formatDuration(r.median_sec)}
                        </TableCell>
                        <TableCell className="tabular-nums text-emerald-600 dark:text-emerald-400">
                          {formatDuration(r.min_sec)}
                        </TableCell>
                        <TableCell className="tabular-nums text-muted-foreground">
                          {formatDuration(r.max_sec)}
                        </TableCell>
                        <TableCell className="tabular-nums">
                          {r.avg_trials.toFixed(1)}
                        </TableCell>
                        <TableCell className="tabular-nums">
                          {r.samples}
                          <span className="ml-1 text-xs text-muted-foreground">
                            ({r.distinct_proxies}p / {r.distinct_machines}m)
                          </span>
                        </TableCell>
                        <TableCell>
                          <Badge className={c.className}>{c.label}</Badge>
                        </TableCell>
                        <TableCell className="tabular-nums">
                          {r.currently_banned > 0 ? (
                            <span className="text-red-600 dark:text-red-400">
                              {r.currently_banned}
                            </span>
                          ) : (
                            "—"
                          )}
                        </TableCell>
                        <TableCell>
                          <ChevronRight className="h-4 w-4 text-muted-foreground" />
                        </TableCell>
                      </TableRow>
                    )
                  })
                )}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <Dialog
        open={openDomain !== null}
        onOpenChange={(o) => {
          if (!o) {
            setOpenDomain(null)
            setDetail(null)
          }
        }}
      >
        <DialogContent className="max-h-[85vh] max-w-5xl overflow-y-auto">
          <DialogHeader>
            <DialogTitle className="font-mono text-base">
              {openDomain}
            </DialogTitle>
            <DialogDescription>
              {selected
                ? estimateSentence(selected)
                : "Observations behind this estimate"}
            </DialogDescription>
          </DialogHeader>

          {selected && (
            <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
              {[
                ["Average", formatDuration(selected.estimated_sec)],
                ["Median", formatDuration(selected.median_sec)],
                ["Fastest", formatDuration(selected.min_sec)],
                ["Slowest", formatDuration(selected.max_sec)],
              ].map(([label, value]) => (
                <div key={label} className="rounded-md border bg-card p-3">
                  <div className="text-xs text-muted-foreground">{label}</div>
                  <div className="text-lg font-semibold tabular-nums">
                    {value}
                  </div>
                </div>
              ))}
            </div>
          )}

          {detailLoading && (
            <div className="py-8 text-center">
              <Loader2 className="mx-auto h-5 w-5 animate-spin" />
            </div>
          )}

          {detailError && (
            <div className="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-600 dark:text-red-400">
              {detailError}
            </div>
          )}

          {detail && (
            <div className="flex flex-col gap-6">
              <section>
                <h3 className="mb-2 text-sm font-semibold">
                  Observed recoveries ({detail.events.length})
                </h3>
                <p className="mb-2 text-xs text-muted-foreground">
                  Each row is one ban that actually lifted. Duration is measured
                  from the ban to the trial that succeeded, so it is an upper
                  bound — the site may have unbanned earlier, between two trials.
                </p>
                <div className="overflow-x-auto rounded-md border">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Proxy</TableHead>
                        <TableHead>Machine</TableHead>
                        <TableHead>Banned at</TableHead>
                        <TableHead>Recovered at</TableHead>
                        <TableHead>Took</TableHead>
                        <TableHead>Failed trials</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {detail.events.length === 0 ? (
                        <TableRow>
                          <TableCell
                            colSpan={6}
                            className="py-6 text-center text-muted-foreground"
                          >
                            No recoveries recorded yet.
                          </TableCell>
                        </TableRow>
                      ) : (
                        detail.events.map((e, i) => (
                          <TableRow key={`${e.proxy_id}-${e.recovered_at}-${i}`}>
                            <TableCell className="font-mono text-xs">
                              {e.proxy_address || `#${e.proxy_id}`}
                            </TableCell>
                            <TableCell className="font-mono text-xs">
                              {e.machine_id}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              {formatWhen(e.banned_at)}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              {formatWhen(e.recovered_at)}
                            </TableCell>
                            <TableCell className="font-semibold tabular-nums">
                              {formatDuration(e.recovery_sec)}
                            </TableCell>
                            <TableCell className="tabular-nums">
                              {e.trials}
                            </TableCell>
                          </TableRow>
                        ))
                      )}
                    </TableBody>
                  </Table>
                </div>
              </section>

              {detail.waiting && detail.waiting.length > 0 && (
                <section>
                  <h3 className="mb-2 text-sm font-semibold">
                    Waiting on this estimate right now ({detail.waiting.length})
                  </h3>
                  <div className="overflow-x-auto rounded-md border">
                    <Table>
                      <TableHeader>
                        <TableRow>
                          <TableHead>Proxy</TableHead>
                          <TableHead>Machine</TableHead>
                          <TableHead>State</TableHead>
                          <TableHead>Next trial in</TableHead>
                          <TableHead>Trials sent</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {detail.waiting.map((c, i) => (
                          <TableRow key={`${c.proxy_id}-${c.machine_id}-${i}`}>
                            <TableCell className="font-mono text-xs">
                              {c.proxy_address}
                            </TableCell>
                            <TableCell className="font-mono text-xs">
                              {c.machine_id}
                            </TableCell>
                            <TableCell className="text-xs">
                              {c.display_state === "recovery_test"
                                ? "Recovery Test"
                                : "Cooldown"}
                            </TableCell>
                            <TableCell className="tabular-nums">
                              {formatDuration(c.cooldown_remaining_sec)}
                            </TableCell>
                            <TableCell className="tabular-nums">
                              {c.probe_attempt}
                            </TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </div>
                </section>
              )}

              {detail.trials && detail.trials.length > 0 && (
                <section>
                  <h3 className="mb-2 text-sm font-semibold">
                    Trial log ({detail.trials.length})
                  </h3>
                  <div className="max-h-72 overflow-y-auto overflow-x-auto rounded-md border">
                    <Table>
                      <TableHeader>
                        <TableRow>
                          <TableHead>When</TableHead>
                          <TableHead>Proxy</TableHead>
                          <TableHead>Machine</TableHead>
                          <TableHead>Result</TableHead>
                          <TableHead>Status</TableHead>
                          <TableHead>Trial #</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {detail.trials.map((t, i) => (
                          <TableRow key={`${t.proxy_id}-${t.attempted_at}-${i}`}>
                            <TableCell className="text-xs text-muted-foreground">
                              {formatWhen(t.attempted_at)}
                            </TableCell>
                            <TableCell className="font-mono text-xs">
                              {t.proxy_address || `#${t.proxy_id}`}
                            </TableCell>
                            <TableCell className="font-mono text-xs">
                              {t.machine_id}
                            </TableCell>
                            <TableCell>
                              <Badge
                                className={cn(
                                  t.result === "pass"
                                    ? "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border border-emerald-500/20"
                                    : "bg-red-500/10 text-red-600 dark:text-red-400 border border-red-500/20"
                                )}
                              >
                                {t.result}
                              </Badge>
                            </TableCell>
                            <TableCell className="tabular-nums text-xs">
                              {t.status_code ?? "—"}
                            </TableCell>
                            <TableCell className="tabular-nums">
                              {t.probe_attempt_after}
                            </TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </div>
                </section>
              )}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}
