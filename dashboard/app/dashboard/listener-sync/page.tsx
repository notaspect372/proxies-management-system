"use client"

import * as React from "react"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Separator } from "@/components/ui/separator"
import { Loader2, Lock, Plus, RotateCw, Trash2 } from "lucide-react"
import { cn } from "@/lib/utils"
import { api } from "@/lib/api"
import { ListenerEntry, ListenerState } from "@/lib/types"

export default function ListenerSyncPage() {
  const [state, setState] = React.useState<ListenerState | null>(null)
  const [loading, setLoading] = React.useState(true)
  const [error, setError] = React.useState<string | null>(null)
  const [restartPending, setRestartPending] = React.useState(false)
  const [deletingPort, setDeletingPort] = React.useState<number | null>(null)

  const load = React.useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setState(await api.getListeners())
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load listeners")
    } finally {
      setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    void load()
  }, [load])

  const handleAdd = React.useCallback(
    async (entry: ListenerEntry) => {
      setError(null)
      try {
        setState(await api.addListener(entry))
        setRestartPending(true)
      } catch (e) {
        setError(e instanceof Error ? e.message : "Add failed")
        throw e
      }
    },
    []
  )

  const handleDelete = React.useCallback(
    async (port: number, label: string) => {
      if (!window.confirm(`Remove ${label} (port ${port})?`)) return
      setDeletingPort(port)
      setError(null)
      try {
        setState(await api.deleteListener(port))
        setRestartPending(true)
      } catch (e) {
        setError(e instanceof Error ? e.message : "Delete failed")
      } finally {
        setDeletingPort(null)
      }
    },
    []
  )

  if (loading && !state) {
    return (
      <div className="flex h-96 items-center justify-center">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (!state) {
    return (
      <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">
        {error ?? "Failed to load listener state"}
      </div>
    )
  }

  // Defensive: a backend that returns null instead of [] would crash .map.
  // Go's encoder usually gives us [], but a future field reorder could regress.
  const entries = state.entries ?? []
  const manual = state.manual ?? []
  const fleetMachines = state.fleet_machines ?? []
  const usedPorts = new Set<number>([
    ...entries.map((e) => e.port),
    ...manual.map((e) => e.port),
  ])

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Aux Listeners</h1>
        <p className="text-muted-foreground">
          Register a (machine, country) → port listener. Saved to{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">AUX_LISTENERS_SHEET</code>{" "}
          in <code className="rounded bg-muted px-1 py-0.5 text-xs">core/.env</code>.
          Restart the server after any change to bind the new ports.
        </p>
      </div>

      {error && (
        <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          {error}
        </div>
      )}

      {restartPending && (
        <div className="flex items-start gap-3 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-sm">
          <RotateCw className="mt-0.5 h-4 w-4 text-amber-600 dark:text-amber-400" />
          <div>
            <div className="font-medium text-amber-700 dark:text-amber-300">
              Restart the server to activate the changes
            </div>
            <div className="mt-0.5 text-muted-foreground">
              Aux listeners only bind their ports at startup. Restart the core
              process to pick up the new entries.
            </div>
          </div>
        </div>
      )}

      <AddListenerCard
        fleetMachines={fleetMachines}
        usedPorts={usedPorts}
        onAdd={handleAdd}
      />

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base">Registered listeners</CardTitle>
          <CardDescription>
            {entries.length}{" "}
            {entries.length === 1 ? "entry" : "entries"} in{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">AUX_LISTENERS_SHEET</code>
          </CardDescription>
        </CardHeader>
        <Separator />
        <CardContent className="p-0">
          <ListenerTable
            entries={entries}
            deletingPort={deletingPort}
            onDelete={handleDelete}
          />
        </CardContent>
      </Card>

      {manual.length > 0 && (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2 text-base">
              <Lock className="h-3.5 w-3.5 text-muted-foreground" />
              Manual entries (read-only)
            </CardTitle>
            <CardDescription>
              {manual.length}{" "}
              {manual.length === 1 ? "entry" : "entries"} from{" "}
              <code className="rounded bg-muted px-1 py-0.5 text-xs">AUX_LISTENERS</code>{" "}
              in <code className="rounded bg-muted px-1 py-0.5 text-xs">core/.env</code>.
              Edit the file directly to change these.
            </CardDescription>
          </CardHeader>
          <Separator />
          <CardContent className="p-0">
            <ListenerTable
              entries={manual}
              deletingPort={null}
              onDelete={() => {}}
              readOnly
            />
          </CardContent>
        </Card>
      )}
    </div>
  )
}

function AddListenerCard({
  fleetMachines,
  usedPorts,
  onAdd,
}: {
  fleetMachines: string[]
  usedPorts: Set<number>
  onAdd: (entry: ListenerEntry) => Promise<void>
}) {
  const [machineId, setMachineId] = React.useState<string>(fleetMachines[0] ?? "")
  const [country, setCountry] = React.useState("")
  const [portStr, setPortStr] = React.useState("")
  const [submitting, setSubmitting] = React.useState(false)

  // When the fleet list arrives, seed the dropdown with the first option so
  // a fresh page load doesn't show an empty select. Doesn't fire again after
  // the user picks something — only the initial empty → first-id transition.
  React.useEffect(() => {
    if (!machineId && fleetMachines.length > 0) {
      setMachineId(fleetMachines[0])
    }
  }, [fleetMachines, machineId])

  const port = portStr.trim() === "" ? NaN : Number(portStr)
  const portError =
    portStr === ""
      ? null
      : !Number.isInteger(port) || port < 1 || port > 65535
        ? "Port must be 1–65535"
        : usedPorts.has(port)
          ? `Port ${port} is already in use`
          : null

  // What's the first thing stopping a successful submit? Surfaced under the
  // button so the user never has to guess why their click "did nothing".
  const blocker = submitting
    ? null
    : machineId === ""
      ? "Pick a machine first"
      : country.trim() === ""
        ? "Enter a country"
        : portStr === ""
          ? "Enter a port"
          : portError

  // Button is only hard-disabled while a request is in flight. Validation
  // failures don't disable — the blocker text under the button explains why
  // a click wouldn't help, so the user can see what to fix.
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (submitting || blocker) return
    setSubmitting(true)
    try {
      await onAdd({
        machine_id: machineId,
        country: country.trim(),
        port,
      })
      setCountry("")
      setPortStr("")
    } catch {
      // surfaced by parent — keep form state so the user can correct
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">Add listener</CardTitle>
        <CardDescription>
          Picks a port on this machine and binds (machine_id, country) routing
          credentials.
        </CardDescription>
      </CardHeader>
      <Separator />
      <CardContent className="p-4">
        <form onSubmit={submit} className="grid gap-3 md:grid-cols-[1fr_1fr_140px_auto]">
          <div className="flex flex-col gap-1">
            <label
              htmlFor="machine"
              className="text-xs font-medium text-muted-foreground"
            >
              Machine
            </label>
            <select
              id="machine"
              value={machineId}
              onChange={(e) => setMachineId(e.target.value)}
              className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            >
              {fleetMachines.length === 0 ? (
                <option value="">No fleet machines loaded</option>
              ) : (
                fleetMachines.map((id) => (
                  <option key={id} value={id}>
                    {id}
                  </option>
                ))
              )}
            </select>
          </div>

          <div className="flex flex-col gap-1">
            <label
              htmlFor="country"
              className="text-xs font-medium text-muted-foreground"
            >
              Country
            </label>
            <Input
              id="country"
              value={country}
              onChange={(e) => setCountry(e.target.value)}
              placeholder="Russia"
              autoComplete="off"
            />
          </div>

          <div className="flex flex-col gap-1">
            <label
              htmlFor="port"
              className="text-xs font-medium text-muted-foreground"
            >
              Port
            </label>
            <Input
              id="port"
              type="number"
              inputMode="numeric"
              min={1}
              max={65535}
              value={portStr}
              onChange={(e) => setPortStr(e.target.value)}
              placeholder="8045"
              className={cn(portError && "border-destructive")}
            />
            {portError && (
              <span className="text-[11px] text-destructive">{portError}</span>
            )}
          </div>

          <div className="flex flex-col items-stretch gap-1 md:items-end">
            <Button
              type="submit"
              disabled={submitting}
              className="w-full md:w-auto"
              title={blocker ?? "Add listener"}
            >
              {submitting ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Plus className="h-3.5 w-3.5" />
              )}
              {submitting ? "Adding" : "Add"}
            </Button>
            {blocker && (
              <span className="text-[11px] text-muted-foreground">{blocker}</span>
            )}
          </div>
        </form>
      </CardContent>
    </Card>
  )
}

function ListenerTable({
  entries,
  deletingPort,
  onDelete,
  readOnly = false,
}: {
  entries: ListenerEntry[]
  deletingPort: number | null
  onDelete: (port: number, label: string) => void
  readOnly?: boolean
}) {
  if (entries.length === 0) {
    return (
      <div className="p-4 text-sm text-muted-foreground">
        No entries yet. Use the form above to register one.
      </div>
    )
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead className="bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
          <tr>
            <th className="px-4 py-2 text-left font-medium">Port</th>
            <th className="px-4 py-2 text-left font-medium">Machine</th>
            <th className="px-4 py-2 text-left font-medium">Country</th>
            {!readOnly && <th className="px-4 py-2 text-right font-medium" />}
          </tr>
        </thead>
        <tbody>
          {entries.map((e) => {
            const machineLabel = e.machine_id || "(default)"
            const label = `${machineLabel}/${e.country}`
            const removing = deletingPort === e.port
            return (
              <tr key={e.port} className="border-t">
                <td className="px-4 py-2 font-mono tabular-nums">{e.port}</td>
                <td className="px-4 py-2 font-mono text-xs">{machineLabel}</td>
                <td className="px-4 py-2">{e.country}</td>
                {!readOnly && (
                  <td className="px-4 py-2 text-right">
                    <button
                      type="button"
                      onClick={() => onDelete(e.port, label)}
                      disabled={removing}
                      title={`Remove ${label}`}
                      aria-label={`Remove ${label}`}
                      className={cn(
                        "inline-flex h-8 w-8 items-center justify-center rounded-md border border-border/40 bg-muted/40 text-foreground/70 transition-colors",
                        "hover:bg-destructive/10 hover:text-destructive hover:border-destructive/40",
                        "disabled:opacity-50 disabled:cursor-not-allowed"
                      )}
                    >
                      {removing ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : (
                        <Trash2 className="h-4 w-4" />
                      )}
                    </button>
                  </td>
                )}
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

