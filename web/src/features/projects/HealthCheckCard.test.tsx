import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HealthCheckCard } from "./HealthCheckCard";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const running = { state: "running" as const, services: [], warnings: [] };

describe("HealthCheckCard", () => {
  it("sets up a check, tests it and shows an outage", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`POST /projects/${id}/health-check/test`]: () => ({ body: { result: { ok: false, status: 302, latencyMs: 12, error: "HTTP 302 instead of 200 (redirect to /login)", url: "https://shop.test/health" } } }),
      [`PUT /projects/${id}/health-check`]: (_u, init) => ({ body: { project: makeProject({ id, healthCheck: JSON.parse(init.body as string) }) } }),
    });
    renderApp(<HealthCheckCard project={makeProject({ id, status: running })} />);
    const user = userEvent.setup();

    expect(screen.getByText("off")).toBeInTheDocument();
    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();
    await user.type(screen.getByLabelText("Path"), "/health");
    await user.click(screen.getByRole("button", { name: "Test" }));
    expect(await screen.findByText(/The check fails: HTTP 302 instead of 200 \(redirect to \/login\)/)).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ path: "/health", status: 200, intervalSec: 30, timeoutSec: 5, failures: 3 });

    const failures = screen.getByLabelText("Down after");
    await user.clear(failures);
    await user.type(failures, "5");
    await user.click(save);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({ path: "/health", status: 200, intervalSec: 30, timeoutSec: 5, failures: 5 });
    expect(await screen.findByText("Saved. The first check runs within a few seconds.")).toBeInTheDocument();
  });

  it("shows the state of a saved check and switches it off", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`PUT /projects/${id}/health-check`]: () => ({ body: { project: makeProject({ id }) } }),
    });
    const project = makeProject({
      id,
      healthCheck: { path: "/up", status: 200, intervalSec: 30, timeoutSec: 5, failures: 3 },
      status: { ...running, health: { state: "down", since: "2026-09-25T10:03:00Z", downSince: "2026-09-25T10:03:00Z", error: "no answer within 5 s", failures: 4 } },
    });
    renderApp(<HealthCheckCard project={project} />);
    const user = userEvent.setup();

    expect(screen.getByText("down")).toBeInTheDocument();
    expect(screen.getByText(/Down since .*: no answer within 5 s/)).toBeInTheDocument();
    expect(screen.getByLabelText("Path")).toHaveValue("/up");
    await user.click(screen.getByRole("button", { name: "Switch off" }));
    await waitFor(() => expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({ path: "" }));
    expect(await screen.findByText("The health check is off.")).toBeInTheDocument();
  });
});
