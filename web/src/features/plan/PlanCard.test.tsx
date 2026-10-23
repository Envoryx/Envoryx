import { screen } from "@testing-library/react";
import { PlanCard } from "./PlanCard";
import { DomainsCard } from "@/features/settings/DomainsCard";
import { OffsiteTargetsCard } from "@/features/offsite/OffsiteTargetsCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const plan = {
  name: "Starter",
  limits: { projects: 3, users: 2, diskGb: 10 },
  runtimes: ["php", "node"],
  disabled: ["offsite", "ideGateway"],
  lockedSettings: ["baseDomain"],
};

describe("PlanCard", () => {
  it("shows the plan, its use and what it leaves out", async () => {
    mockApi({
      ...authedRoutes,
      "GET /plan": () => ({ body: { plan, usage: { projects: 3, users: 1, diskBytes: 2 * 2 ** 30, diskMeasuredAt: "2026-10-10T08:00:00Z" } } }),
    });
    renderApp(<PlanCard />);

    expect(await screen.findByText("Starter")).toBeInTheDocument();
    expect(screen.getByText("3 of 3")).toBeInTheDocument();
    expect(screen.getByText("1 of 2")).toBeInTheDocument();
    expect(screen.getByText("2.0 GiB of 10 GiB")).toBeInTheDocument();
    expect(screen.getByText("PHP, Node.js")).toBeInTheDocument();
    expect(screen.getByText("Offsite targets")).toBeInTheDocument();
    expect(screen.getByText("IDE gateway")).toBeInTheDocument();
    expect(screen.getByText("Base domain")).toBeInTheDocument();
  });

  it("shows the services, the running projects and the container limits", async () => {
    mockApi({
      ...authedRoutes,
      "GET /plan": () => ({
        body: {
          plan: { ...plan, services: ["mariadb", "redis", "storage"], limits: { ...plan.limits, runningProjects: 2, container: { cpus: 2, memoryMb: 1024 }, containerDefault: { memoryMb: 512 } } },
          usage: { projects: 3, users: 1, runningProjects: 1, diskBytes: 0 },
        },
      }),
    });
    renderApp(<PlanCard />);

    expect(await screen.findByText("MariaDB, Redis, Object storage (S3)")).toBeInTheDocument();
    expect(screen.getByText("Running projects")).toBeInTheDocument();
    expect(screen.getAllByText("1 of 2")).toHaveLength(2); // users and running projects
    expect(screen.getByText("at most 2 cores, 1.0 GiB; 512 MiB where the project sets none")).toBeInTheDocument();
  });

  it("shows the fleet manager the instance belongs to", async () => {
    mockApi({
      ...authedRoutes,
      "GET /plan": () => ({ body: { plan, usage: { projects: 0, users: 1, diskBytes: 0 }, fleet: { url: "https://fleet.example.net", name: "Kunde 1", connected: false, lastContact: "2026-10-10T08:00:00Z", error: "fleet manager connection: EOF" } } }),
    });
    renderApp(<PlanCard />);

    expect(await screen.findByText("Managed by your hoster as Kunde 1")).toBeInTheDocument();
    expect(screen.getByText("not connected")).toBeInTheDocument();
    expect(screen.getByText("fleet manager connection: EOF")).toBeInTheDocument();
  });

  it("shows nothing on an instance without a plan", async () => {
    const api = mockApi({ ...authedRoutes, "GET /plan": () => ({ body: { plan: null } }) });
    const { container } = renderApp(<PlanCard />);
    await vi.waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/plan"))).toBe(true));
    expect(container).toBeEmptyDOMElement();
  });

  it("locks the settings the plan fixes", async () => {
    mockApi({
      ...authedRoutes,
      "GET /settings/tls": () => ({ body: { enabled: false, proxy: { enabled: false }, baseDomain: "c1.example.net", forceHttps: false } }),
      "GET /settings": () => ({ body: { publicHost: "", baseDomain: "c1.example.net", forceHttps: false, proxy: { enabled: false }, lockedSettings: ["baseDomain"] } }),
    });
    renderApp(<DomainsCard />);

    const input = await screen.findByLabelText("Base domain");
    expect(input).toBeDisabled();
    expect(screen.getByText("Set by your hoster")).toBeInTheDocument();
  });

  it("explains a feature the plan leaves out", async () => {
    mockApi({
      ...authedRoutes,
      "GET /plan": () => ({ body: { plan, usage: { projects: 0, users: 1, diskBytes: 0 } } }),
      "GET /offsite": () => ({ body: { targets: [] } }),
    });
    renderApp(<OffsiteTargetsCard />);

    expect(await screen.findByText("Not included in your plan")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add target" })).not.toBeInTheDocument();
  });
});
