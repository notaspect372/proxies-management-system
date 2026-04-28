/**
 * Starts TimescaleDB + Go API via Docker Compose (repo root), waits for :8001/health, then runs `next dev`.
 */
import { execSync, spawn } from "node:child_process"
import fs from "node:fs"
import { fileURLToPath } from "node:url"
import path from "node:path"

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const dashboardDir = path.resolve(__dirname, "..")
const repoRoot = path.resolve(dashboardDir, "..")
const nextCli = path.join(dashboardDir, "node_modules", "next", "dist", "bin", "next")

function startGoBackendIfRequested() {
  const driver = (process.env.DB_DRIVER || "").toLowerCase()
  if (driver !== "mongo") return null

  if (!process.env.MONGO_URI) {
    console.error(
      "\n\x1b[33m[dev]\x1b[0m DB_DRIVER=mongo set but MONGO_URI is missing.\n" +
        "Set env vars, then retry:\n" +
        `  $env:DB_DRIVER="mongo"\n` +
        `  $env:MONGO_URI="mongodb+srv://..."\n` +
        `  $env:MONGO_DB="rota"\n`
    )
    process.exit(1)
  }

  console.log("\n\x1b[36m[dev]\x1b[0m Starting Go backend locally (MongoDB Atlas)…\n")
  const goDir = path.resolve(repoRoot, "core")
  const child = spawn("go", ["run", "./cmd/server"], {
    cwd: goDir,
    stdio: "inherit",
    env: process.env,
  })

  const cleanup = () => {
    try {
      child.kill("SIGINT")
    } catch {
      // ignore
    }
  }
  process.on("exit", cleanup)
  process.on("SIGINT", cleanup)
  process.on("SIGTERM", cleanup)

  return child
}

function runDockerUp() {
  console.log("\n\x1b[36m[dev]\x1b[0m Starting database + API (docker compose)…\n")
  try {
    execSync("docker compose up -d timescaledb rota-core", {
      cwd: repoRoot,
      stdio: "inherit",
      env: process.env,
    })
  } catch {
    console.error(
      "\n\x1b[33m[dev]\x1b[0m docker compose failed. Install Docker Desktop or start the stack yourself:\n" +
        `  cd "${repoRoot}"\n` +
        "  docker compose up -d timescaledb rota-core\n" +
        "Then run UI only: npm run dev:ui\n"
    )
    process.exit(1)
  }
}

async function fetchHealth(url) {
  const ac = new AbortController()
  const t = setTimeout(() => ac.abort(), 2500)
  try {
    const res = await fetch(url, { signal: ac.signal })
    return res.ok
  } catch {
    return false
  } finally {
    clearTimeout(t)
  }
}

async function waitForApi(timeoutMs = 180_000) {
  const url = "http://127.0.0.1:8001/health"
  const start = Date.now()
  process.stdout.write("\x1b[36m[dev]\x1b[0m Waiting for API on :8001 ")
  while (Date.now() - start < timeoutMs) {
    if (await fetchHealth(url)) {
      console.log(" ready.\n")
      return true
    }
    process.stdout.write(".")
    await new Promise((r) => setTimeout(r, 1000))
  }
  console.log("\n")
  console.error(
    "\x1b[33m[dev]\x1b[0m API did not respond on http://127.0.0.1:8001/health in time.\n" +
      "Check logs:\n" +
      `  cd "${repoRoot}"\n` +
      "  docker compose logs rota-core\n"
  )
  return false
}

function runNextDev() {
  if (!fs.existsSync(nextCli)) {
    console.error(
      "Next.js CLI not found. Install dependencies in dashboard/:\n  cd dashboard && npm install"
    )
    process.exit(1)
  }
  const child = spawn(process.execPath, ["--no-deprecation", nextCli, "dev", ...process.argv.slice(2)], {
    cwd: dashboardDir,
    stdio: "inherit",
    env: process.env,
  })
  child.on("exit", (code) => process.exit(code ?? 0))
}

const backend = startGoBackendIfRequested()
if (!backend) runDockerUp()
const ok = await waitForApi()
if (!ok) process.exit(1)
runNextDev()
