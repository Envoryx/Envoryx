import { spawn, execFile, type ChildProcess } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, existsSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { port } from "../playwright.config";

const execFileP = promisify(execFile);

/** The project every spec works with; global teardown removes its Docker leftovers. */
export const projectName = "E2E Shop";
export const projectSlug = "e2e-shop";

const here = fileURLToPath(new URL(".", import.meta.url));
const bin = process.env.ENVORYX_E2E_BIN ?? resolve(here, "../../bin/envoryx");
// Server output ends up next to the Playwright traces.
const serverLog = resolve(here, "../test-results/envoryx.log");
const base = `http://127.0.0.1:${port}`;

async function waitForHealth(child: ChildProcess): Promise<void> {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`envoryx exited with code ${child.exitCode} during start-up`);
    try {
      const res = await fetch(`${base}/api/v1/health`);
      if (res.ok) return;
    } catch {
      /* not listening yet */
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`envoryx did not answer on ${base} within 30 s`);
}

/** Removes Docker resources of the test project that a failed spec may have left behind. */
async function removeProjectLeftovers(): Promise<void> {
  const filter = `label=envoryx.project.name=${projectSlug}`;
  for (const [list, remove] of [
    [["ps", "-aq", "--filter", filter], ["rm", "-f"]],
    [["network", "ls", "-q", "--filter", filter], ["network", "rm"]],
    [["volume", "ls", "-q", "--filter", filter], ["volume", "rm"]],
  ] as const) {
    try {
      const { stdout } = await execFileP("docker", [...list]);
      const ids = stdout.split("\n").filter(Boolean);
      if (ids.length > 0) await execFileP("docker", [...remove, ...ids]);
    } catch {
      /* no docker CLI on this host, or nothing to remove */
    }
  }
}

export default async function globalSetup() {
  if (!existsSync(bin)) {
    throw new Error(`${bin} not found – run "make build" first (or set ENVORYX_E2E_BIN)`);
  }
  const work = mkdtempSync(join(tmpdir(), "envoryx-e2e-"));
  const config = join(work, "config");
  const projects = join(work, "projects");
  mkdirSync(config);
  mkdirSync(projects);

  const rangeStart = Number(process.env.ENVORYX_E2E_PORT_RANGE_START ?? 25000);
  const child = spawn(bin, ["serve"], {
    env: {
      ...process.env,
      ENVORYX_LISTEN: `127.0.0.1:${port}`,
      ENVORYX_CONFIG_DIR: config,
      ENVORYX_PROJECTS_DIR: projects,
      ENVORYX_PORT_RANGE_START: String(rangeStart),
      ENVORYX_PORT_RANGE_END: String(rangeStart + 99),
      // No proxy/SSH listeners: privileged ports, and nothing here needs them.
      ENVORYX_PROXY_HTTP: "",
      ENVORYX_PROXY_HTTPS: "",
      ENVORYX_SSH: "",
      ENVORYX_UPDATE_CHECK: "false",
      ENVORYX_LOG_FORMAT: "text",
      PUID: String(process.getuid?.() ?? 1000),
      PGID: String(process.getgid?.() ?? 1000),
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const chunks: Buffer[] = [];
  child.stdout?.on("data", (c: Buffer) => chunks.push(c));
  child.stderr?.on("data", (c: Buffer) => chunks.push(c));

  try {
    await waitForHealth(child);
  } catch (err) {
    child.kill("SIGKILL");
    console.error(Buffer.concat(chunks).toString());
    rmSync(work, { recursive: true, force: true });
    throw err;
  }
  console.log(`envoryx ${bin} listening on ${base}, data in ${work}`);
  // Visible to the specs (Playwright passes the environment on to its workers).
  process.env.ENVORYX_E2E_PROJECTS_DIR = projects;

  return async () => {
    const exited = new Promise<void>((r) => child.once("exit", () => r()));
    child.kill("SIGTERM");
    await Promise.race([exited, new Promise((r) => setTimeout(r, 15_000))]);
    if (child.exitCode === null) child.kill("SIGKILL");
    await removeProjectLeftovers();
    mkdirSync(resolve(serverLog, ".."), { recursive: true });
    writeFileSync(serverLog, Buffer.concat(chunks));
    if (process.env.ENVORYX_E2E_KEEP) {
      console.log(`kept ${work} (ENVORYX_E2E_KEEP)`);
    } else {
      rmSync(work, { recursive: true, force: true });
    }
  };
}
