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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Loader2,
  Lock,
  Pencil,
  Pin,
  Plus,
  RotateCw,
  Shuffle,
  Trash2,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { api } from "@/lib/api"
import { ListenerEntry, ListenerMode, ListenerState } from "@/lib/types"

function modeOf(e: ListenerEntry): ListenerMode {
  return e.mode === "rotate" ? "rotate" : "sticky"
}

export default function ListenerSyncPage() {
  const [state, setState] = React.useState<ListenerState | null>(null)
  const [loading, setLoading] = React.useState(true)
  const [error, setError] = React.useState<string | null>(null)
  const [restartPending, setRestartPending] = React.useState(false)
  const [deletingPort, setDeletingPort] = React.useState<number | null>(null)
  const [modeChangingPort, setModeChangingPort] = React.useState<number | null>(null)
  const [isAddDialogOpen, setIsAddDialogOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<ListenerEntry | null>(null)

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
        setIsAddDialogOpen(false)
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

  const handleToggleMode = React.useCallback(
    async (port: number, current: ListenerMode) => {
      const next: ListenerMode = current === "sticky" ? "rotate" : "sticky"
      setModeChangingPort(port)
      setError(null)
      try {
        setState(await api.setListenerMode(port, next))
        setRestartPending(true)
      } catch (e) {
        setError(e instanceof Error ? e.message : "Mode change failed")
      } finally {
        setModeChangingPort(null)
      }
    },
    []
  )

  const handleEdit = React.useCallback(
    async (originalPort: number, patch: Partial<ListenerEntry>) => {
      setError(null)
      try {
        setState(await api.updateListener(originalPort, patch))
        setRestartPending(true)
        setEditing(null)
      } catch (e) {
        setError(e instanceof Error ? e.message : "Update failed")
        throw e
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

      <Card>
        <CardHeader className="pb-3">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <CardTitle className="text-base">Registered listeners</CardTitle>
              <CardDescription>
                {entries.length}{" "}
                {entries.length === 1 ? "entry" : "entries"} in{" "}
                <code className="rounded bg-muted px-1 py-0.5 text-xs">AUX_LISTENERS_SHEET</code>
              </CardDescription>
            </div>
            <Button
              size="sm"
              onClick={() => setIsAddDialogOpen(true)}
              disabled={loading || fleetMachines.length === 0}
              title={
                fleetMachines.length === 0
                  ? "Fleet machines haven't loaded yet"
                  : "Add a new listener"
              }
            >
              <Plus className="h-3.5 w-3.5" />
              Add Listener
            </Button>
          </div>
        </CardHeader>
        <Separator />
        <CardContent className="p-0">
          <ListenerTable
            entries={entries}
            deletingPort={deletingPort}
            modeChangingPort={modeChangingPort}
            onDelete={handleDelete}
            onToggleMode={handleToggleMode}
            onEdit={setEditing}
          />
        </CardContent>
      </Card>

      <AddListenerDialog
        open={isAddDialogOpen}
        onOpenChange={setIsAddDialogOpen}
        fleetMachines={fleetMachines}
        usedPorts={usedPorts}
        onAdd={handleAdd}
      />

      <EditListenerDialog
        entry={editing}
        onOpenChange={(open) => {
          if (!open) setEditing(null)
        }}
        fleetMachines={fleetMachines}
        usedPorts={usedPorts}
        onSave={handleEdit}
      />

      {manual.length > 0 && (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2 text-base">
              <Lock className="h-3.5 w-3.5 text-muted-foreground" />
              Manual entries
            </CardTitle>
            <CardDescription>
              {manual.length}{" "}
              {manual.length === 1 ? "entry" : "entries"} originally added by
              hand to{" "}
              <code className="rounded bg-muted px-1 py-0.5 text-xs">AUX_LISTENERS</code>{" "}
              in <code className="rounded bg-muted px-1 py-0.5 text-xs">core/.env</code>.
              Delete works here too — new entries still go through the button
              above.
            </CardDescription>
          </CardHeader>
          <Separator />
          <CardContent className="p-0">
            <ListenerTable
              entries={manual}
              deletingPort={deletingPort}
              modeChangingPort={modeChangingPort}
              onDelete={handleDelete}
              onToggleMode={handleToggleMode}
              onEdit={setEditing}
            />
          </CardContent>
        </Card>
      )}
    </div>
  )
}

function EditListenerDialog({
  entry,
  onOpenChange,
  fleetMachines,
  usedPorts,
  onSave,
}: {
  entry: ListenerEntry | null
  onOpenChange: (open: boolean) => void
  fleetMachines: string[]
  usedPorts: Set<number>
  onSave: (originalPort: number, patch: Partial<ListenerEntry>) => Promise<void>
}) {
  const open = entry !== null
  // Snapshot the entry the FIRST time this dialog opens for a given port,
  // and keep it stable while open — otherwise a background refresh could
  // yank the input state from under the user mid-edit.
  const [machineId, setMachineId] = React.useState("")
  const [country, setCountry] = React.useState("")
  const [portStr, setPortStr] = React.useState("")
  const [mode, setMode] = React.useState<ListenerMode>("sticky")
  const [submitting, setSubmitting] = React.useState(false)
  const [dialogError, setDialogError] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (entry) {
      setMachineId(entry.machine_id)
      setCountry(entry.country)
      setPortStr(String(entry.port))
      setMode(modeOf(entry))
      setSubmitting(false)
      setDialogError(null)
    }
  }, [entry])

  const port = portStr.trim() === "" ? NaN : Number(portStr)
  const portError =
    portStr === ""
      ? "Enter a port"
      : !Number.isInteger(port) || port < 1 || port > 65535
        ? "Port must be 1–65535"
        : // Allow keeping the same port; block only collisions with OTHER entries.
          entry && port !== entry.port && usedPorts.has(port)
          ? `Port ${port} is already in use`
          : null

  const blocker = submitting
    ? null
    : machineId === ""
      ? "Pick a machine"
      : country.trim() === ""
        ? "Enter a country"
        : portError

  // What actually changed vs the original entry? Only send those fields.
  const patch = React.useMemo<Partial<ListenerEntry>>(() => {
    if (!entry) return {}
    const p: Partial<ListenerEntry> = {}
    if (machineId !== entry.machine_id) p.machine_id = machineId
    if (country.trim() !== entry.country) p.country = country.trim()
    if (Number.isFinite(port) && port !== entry.port) p.port = port
    if (mode !== modeOf(entry)) p.mode = mode
    return p
  }, [entry, machineId, country, port, mode])

  const nothingChanged = Object.keys(patch).length === 0

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!entry || submitting || blocker) return
    if (nothingChanged) {
      onOpenChange(false)
      return
    }
    setSubmitting(true)
    setDialogError(null)
    // Diagnostic: this proves the click reached the handler and shows what
    // we're sending. Check the Network tab for the actual PATCH response.
    console.log("[editListener] PATCH", entry.port, patch)
    try {
      await onSave(entry.port, patch)
    } catch (err) {
      // Keep the dialog open so the user can see the actual server error
      // (a hidden toast/banner behind the modal is easy to miss).
      setDialogError(err instanceof Error ? err.message : "Update failed")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit listener</DialogTitle>
          <DialogDescription>
            {entry ? (
              <>
                Original: <code className="rounded bg-muted px-1 py-0.5 text-xs">
                  {entry.machine_id}/{entry.country}:{entry.port} ({modeOf(entry)})
                </code>
              </>
            ) : (
              "Modify machine, country, port, or mode. Restart required for changes to bind."
            )}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4 py-2">
          <div className="grid gap-2">
            <label
              htmlFor="edit-machine"
              className="text-xs font-medium text-muted-foreground"
            >
              Machine
            </label>
            <select
              id="edit-machine"
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
              {machineId && !fleetMachines.includes(machineId) && (
                <option value={machineId}>{machineId} (not in fleet)</option>
              )}
            </select>
          </div>

          <div className="grid gap-2">
            <label
              htmlFor="edit-country"
              className="text-xs font-medium text-muted-foreground"
            >
              Country
            </label>
            <Input
              id="edit-country"
              value={country}
              onChange={(e) => setCountry(e.target.value)}
              placeholder="Russia"
              autoComplete="off"
            />
          </div>

          <div className="grid gap-2">
            <label
              htmlFor="edit-port"
              className="text-xs font-medium text-muted-foreground"
            >
              Port
            </label>
            <Input
              id="edit-port"
              type="number"
              inputMode="numeric"
              min={1}
              max={65535}
              value={portStr}
              onChange={(e) => setPortStr(e.target.value)}
              className={cn(portError && "border-destructive")}
            />
            {portError && (
              <span className="text-[11px] text-destructive">{portError}</span>
            )}
          </div>

          <div className="grid gap-2">
            <span className="text-xs font-medium text-muted-foreground">Mode</span>
            <div className="grid grid-cols-2 gap-2">
              <ModeOption
                value="sticky"
                current={mode}
                onSelect={setMode}
                icon={<Pin className="h-3.5 w-3.5" />}
                title="Sticky"
                description="Same proxy per (machine, domain)."
              />
              <ModeOption
                value="rotate"
                current={mode}
                onSelect={setMode}
                icon={<Shuffle className="h-3.5 w-3.5" />}
                title="Rotate"
                description="Fresh random proxy per request."
              />
            </div>
          </div>

          {dialogError && (
            <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {dialogError}
            </div>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={submitting}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={submitting || !!blocker || nothingChanged}
              title={
                blocker
                  ? blocker
                  : nothingChanged
                    ? "Nothing changed"
                    : "Save changes"
              }
            >
              {submitting ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Pencil className="h-3.5 w-3.5" />
              )}
              {submitting ? "Saving" : "Save"}
            </Button>
          </DialogFooter>
          {(blocker || nothingChanged) && (
            <span className="block text-right text-[11px] text-muted-foreground">
              {blocker ?? (nothingChanged ? "Nothing changed" : "")}
            </span>
          )}
        </form>
      </DialogContent>
    </Dialog>
  )
}

function AddListenerDialog({
  open,
  onOpenChange,
  fleetMachines,
  usedPorts,
  onAdd,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  fleetMachines: string[]
  usedPorts: Set<number>
  onAdd: (entry: ListenerEntry) => Promise<void>
}) {
  const [machineId, setMachineId] = React.useState<string>(fleetMachines[0] ?? "")
  const [country, setCountry] = React.useState("")
  const [portStr, setPortStr] = React.useState("")
  const [mode, setMode] = React.useState<ListenerMode>("sticky")
  const [submitting, setSubmitting] = React.useState(false)

  // When the fleet list arrives, seed the dropdown with the first option so
  // a fresh page load doesn't show an empty select. Doesn't fire again after
  // the user picks something — only the initial empty → first-id transition.
  React.useEffect(() => {
    if (!machineId && fleetMachines.length > 0) {
      setMachineId(fleetMachines[0])
    }
  }, [fleetMachines, machineId])

  // Reset the form each time the dialog opens so a previous half-filled
  // attempt doesn't leak into the next add. Machine keeps its selection
  // (first fleet id) so power users can add several entries in a row.
  React.useEffect(() => {
    if (open) {
      setCountry("")
      setPortStr("")
      setMode("sticky")
      setSubmitting(false)
    }
  }, [open])

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
        mode,
      })
      setCountry("")
      setPortStr("")
      setMode("sticky")
    } catch {
      // surfaced by parent — keep form state so the user can correct
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add listener</DialogTitle>
          <DialogDescription>
            Opens a dedicated port that binds (machine_id, country) routing
            credentials for scrapers.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4 py-2">
          <div className="grid gap-2">
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

          <div className="grid gap-2">
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
              autoFocus
            />
          </div>

          <div className="grid gap-2">
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

          <div className="grid gap-2">
            <span className="text-xs font-medium text-muted-foreground">Mode</span>
            <div className="grid grid-cols-2 gap-2">
              <ModeOption
                value="sticky"
                current={mode}
                onSelect={setMode}
                icon={<Pin className="h-3.5 w-3.5" />}
                title="Sticky"
                description="Same proxy per (machine, domain). Best for session-like scraping."
              />
              <ModeOption
                value="rotate"
                current={mode}
                onSelect={setMode}
                icon={<Shuffle className="h-3.5 w-3.5" />}
                title="Rotate"
                description="Fresh random proxy per request, never the same IP twice in a row."
              />
            </div>
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={submitting}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={submitting}
              title={blocker ?? "Add listener"}
            >
              {submitting ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Plus className="h-3.5 w-3.5" />
              )}
              {submitting ? "Adding" : "Add"}
            </Button>
          </DialogFooter>
          {blocker && (
            <span className="block text-right text-[11px] text-muted-foreground">
              {blocker}
            </span>
          )}
        </form>
      </DialogContent>
    </Dialog>
  )
}

function ModeOption({
  value,
  current,
  onSelect,
  icon,
  title,
  description,
}: {
  value: ListenerMode
  current: ListenerMode
  onSelect: (mode: ListenerMode) => void
  icon: React.ReactNode
  title: string
  description: string
}) {
  const selected = current === value
  return (
    <button
      type="button"
      onClick={() => onSelect(value)}
      className={cn(
        "flex flex-col items-start gap-1 rounded-lg border p-3 text-left transition-colors",
        selected
          ? "border-primary/60 bg-primary/5 shadow-sm"
          : "border-border hover:bg-accent"
      )}
      aria-pressed={selected}
    >
      <div className="flex items-center gap-1.5 text-sm font-medium">
        {icon}
        {title}
      </div>
      <span className="text-[11px] leading-snug text-muted-foreground">
        {description}
      </span>
    </button>
  )
}

function ListenerTable({
  entries,
  deletingPort,
  modeChangingPort,
  onDelete,
  onToggleMode,
  onEdit,
}: {
  entries: ListenerEntry[]
  deletingPort: number | null
  modeChangingPort: number | null
  onDelete: (port: number, label: string) => void
  onToggleMode: (port: number, current: ListenerMode) => void
  onEdit: (entry: ListenerEntry) => void
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
            <th className="px-4 py-2 text-left font-medium">Mode</th>
            <th className="px-4 py-2 text-right font-medium" />
          </tr>
        </thead>
        <tbody>
          {entries.map((e) => {
            const machineLabel = e.machine_id || "(default)"
            const label = `${machineLabel}/${e.country}`
            const removing = deletingPort === e.port
            const changingMode = modeChangingPort === e.port
            const mode = modeOf(e)
            return (
              <tr key={e.port} className="border-t">
                <td className="px-4 py-2 font-mono tabular-nums">{e.port}</td>
                <td className="px-4 py-2 font-mono text-xs">{machineLabel}</td>
                <td className="px-4 py-2">{e.country}</td>
                <td className="px-4 py-2">
                  <button
                    type="button"
                    onClick={() => onToggleMode(e.port, mode)}
                    disabled={changingMode}
                    title={
                      mode === "sticky"
                        ? "Sticky — same proxy per (machine, domain). Click to switch to rotate."
                        : "Rotate — fresh random proxy per request. Click to switch to sticky."
                    }
                    className={cn(
                      "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
                      mode === "rotate"
                        ? "border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300 hover:bg-sky-500/20"
                        : "border-border bg-muted/60 text-foreground/80 hover:bg-muted",
                      "disabled:opacity-50 disabled:cursor-not-allowed"
                    )}
                  >
                    {changingMode ? (
                      <Loader2 className="h-3 w-3 animate-spin" />
                    ) : mode === "rotate" ? (
                      <Shuffle className="h-3 w-3" />
                    ) : (
                      <Pin className="h-3 w-3" />
                    )}
                    {mode === "rotate" ? "Rotate" : "Sticky"}
                  </button>
                </td>
                <td className="px-4 py-2 text-right">
                  <div className="inline-flex items-center gap-1">
                    <button
                      type="button"
                      onClick={() => onEdit(e)}
                      title={`Edit ${label}`}
                      aria-label={`Edit ${label}`}
                      className={cn(
                        "inline-flex h-8 w-8 items-center justify-center rounded-md border border-border/40 bg-muted/40 text-foreground/70 transition-colors",
                        "hover:bg-primary/10 hover:text-primary hover:border-primary/40"
                      )}
                    >
                      <Pencil className="h-4 w-4" />
                    </button>
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
                  </div>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

