import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LimitsCard } from "./LimitsCard";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";

describe("LimitsCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows the limits, saves changes and the usage against them", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/stats`]: () => ({
        body: {
          stats: { cpuPercent: 80, memoryBytes: 900 << 20, running: 2, containers: 2 },
          sampledAt: "2026-09-24T10:00:00Z",
          host: { cpus: 8, memory: 32 << 30 },
          containers: [
            { containerId: "c1", name: "envoryx-acme-shop-php", service: "php", group: "app", cpuPercent: 75, memoryBytes: 900 << 20, cpuLimit: 1.5, memLimit: 1 << 30 },
            { containerId: "c2", name: "envoryx-acme-shop-database", service: "database", group: "services", cpuPercent: 5, memoryBytes: 200 << 20, cpuLimit: 0, memLimit: 0 },
          ],
        },
      }),
      [`PUT /projects/${id}/limits`]: (_u, init) => ({ body: { project: makeProject({ id, limits: JSON.parse(init.body as string) }) } }),
    });
    const project = makeProject({ id, limits: { app: { cpus: 1.5, memoryMb: 1024 }, services: {} } });
    renderApp(<LimitsCard project={project} />);
    const user = userEvent.setup();

    expect(await screen.findByText(/This host has 8 cores/)).toBeInTheDocument();
    const cpus = screen.getAllByLabelText("CPU cores per container");
    const memory = screen.getAllByLabelText("Memory per container");
    expect(cpus[0]).toHaveValue("1.5");
    expect(memory[0]).toHaveValue("1");
    expect(screen.getByText("CPU 0.75 of 1.5 cores")).toBeInTheDocument();
    expect(screen.getByText("Memory 200 MB (no limit)")).toBeInTheDocument();

    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();
    await user.type(memory[1]!, "512");
    await user.selectOptions(screen.getAllByLabelText("Unit")[1]!, "MiB");
    await user.type(screen.getByLabelText("Processes per container"), "2048");
    await user.click(save);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({ app: { cpus: 1.5, memoryMb: 1024 }, services: { memoryMb: 512 }, pids: 2048 });
    expect(await screen.findByText(/Limits saved and applied/)).toBeInTheDocument();
  });
});
