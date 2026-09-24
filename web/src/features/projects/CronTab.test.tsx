import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CronTab } from "./CronTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const job = {
  id: "j1", name: "report", runtime: "php", schedule: "30 3 * * *", command: "php artisan report", timeoutSeconds: 600, enabled: true,
  nextRun: "2026-09-25T03:30:00Z", running: false, runtimeMissing: false, createdAt: "2026-09-24T10:00:00Z",
  lastRun: { id: "r1", source: "schedule", status: "failed", exitCode: 2, startedAt: "2026-09-24T03:30:00Z", finishedAt: "2026-09-24T03:30:04Z" },
};

describe("CronTab", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("lists jobs, runs one and shows its output", async () => {
    const api = mockApi({
      ...authedRoutes,
      // Routes match by prefix: the runs route has to come before the list.
      [`GET /projects/${id}/cron/j1/runs`]: () => ({ body: { runs: [{ id: "r2", source: "manual", status: "succeeded", exitCode: 0, startedAt: "2026-09-24T10:00:00Z", finishedAt: "2026-09-24T10:00:02Z", output: "report sent\n", truncated: false }] } }),
      [`GET /projects/${id}/cron`]: () => ({ body: { jobs: [job], timezone: "Europe/Berlin" } }),
      [`POST /projects/${id}/cron/j1/run`]: () => ({ status: 202, body: { run: { id: "r2", source: "manual", status: "running", exitCode: 0, startedAt: "2026-09-24T10:00:00Z" } } }),
      "POST /cron/preview": () => ({ body: { next: ["2026-09-25T03:00:00Z"], timezone: "Europe/Berlin" } }),
    });
    renderApp(<CronTab project={makeProject()} />);
    const user = userEvent.setup();

    expect(await screen.findByText("report")).toBeInTheDocument();
    expect(screen.getByText(/daily at 03:30/)).toBeInTheDocument();
    expect(screen.getByText(/Europe\/Berlin/)).toBeInTheDocument();
    expect(screen.getByText("failed (2)")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Run now" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith("/cron/j1/run"))).toBe(true));
    await user.click(await screen.findByRole("button", { name: /succeeded/ }));
    expect(await screen.findByText("report sent")).toBeInTheDocument();
  });

  it("adds a job from a template with the schedule the form builds", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/cron`]: () => ({ body: { jobs: [], timezone: "UTC" } }),
      [`POST /projects/${id}/cron`]: (_u, init) => ({ status: 201, body: { job: { ...job, ...JSON.parse(init.body as string), id: "j2" } } }),
      "POST /cron/preview": () => ({ body: { next: ["2026-09-24T10:01:00Z"], timezone: "UTC" } }),
    });
    renderApp(<CronTab project={makeProject()} />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Laravel scheduler" }));
    expect(screen.getByLabelText("Schedule")).toHaveValue("minute");
    await user.selectOptions(screen.getByLabelText("Schedule"), "every");
    await user.selectOptions(screen.getByLabelText("Interval"), "15");
    await user.click(screen.getByRole("button", { name: "Add cron job" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith(`/projects/${id}/cron`))).toBe(true));
    const body = api.calls.find((c) => c.method === "POST" && c.url.endsWith(`/projects/${id}/cron`))!.body as Record<string, unknown>;
    expect(body).toEqual({ name: "scheduler", runtime: "php", schedule: "*/15 * * * *", command: "php artisan schedule:run", timeoutSeconds: 600, enabled: true });
    expect(await screen.findByText('Cron job "scheduler" added.')).toBeInTheDocument();
  });
});
