import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { WorkersTab } from "./WorkersTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const presets = [
  { id: "laravel:queue", group: "Laravel", label: "Queue worker", description: "queue:work", argLabel: "Queues", argHint: "e.g. default" },
  { id: "laravel:schedule", group: "Laravel", label: "Scheduler", description: "schedule:work" },
];

describe("WorkersTab", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("adds, disables and removes workers", async () => {
    let workers = [{ id: "w1", name: "cron", preset: "laravel:schedule", arg: "", enabled: true, command: ["php", "artisan", "schedule:work"], createdAt: "2026-09-18T10:00:00Z" }];
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/workers`]: () => ({ body: { workers, presets } }),
      [`POST /projects/${id}/workers`]: (_u, init) => {
        const body = JSON.parse(init.body as string);
        const w = { id: "w2", ...body, command: ["php", "artisan", "queue:work", `--queue=${body.arg}`], createdAt: "2026-09-18T10:00:00Z" };
        workers = [...workers, w];
        return { status: 201, body: { worker: w } };
      },
      [`PUT /projects/${id}/workers/w1`]: (_u, init) => ({ body: { worker: { ...workers[0], ...JSON.parse(init.body as string) } } }),
      [`DELETE /projects/${id}/workers/w1`]: () => ({ status: 204 }),
    });
    const project = makeProject();
    project.status.services.push({ kind: "worker", variant: "cron", version: "laravel:schedule", image: "x", containerName: "envoryx-acme-shop-worker-cron", exists: true, running: true, state: "running", ports: [], workerId: "w1", imagePrevious: false, imagePinned: false });
    renderApp(<WorkersTab project={project} />);
    const user = userEvent.setup();

    expect(await screen.findByText("cron")).toBeInTheDocument();
    expect(screen.getByText("php artisan schedule:work")).toBeInTheDocument();

    await user.type(screen.getByLabelText("Name"), "queue");
    await user.selectOptions(screen.getByLabelText("Preset"), "laravel:queue");
    await user.type(screen.getByLabelText("Queues"), "emails");
    await user.click(screen.getByRole("button", { name: "Add worker" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST")).toBe(true));
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ name: "queue", preset: "laravel:queue", arg: "emails", enabled: true });
    expect(await screen.findByText(/Worker "queue" added/)).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "Disable" })[0]!);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toMatchObject({ name: "cron", enabled: false });

    await user.click(screen.getByRole("button", { name: "Remove cron" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE")).toBe(true));
  });
});
