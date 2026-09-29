import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes, useLocation } from "react-router-dom";
import { ProjectDetailPage } from "./ProjectDetailPage";
import { authedRoutes, makeProject, mockApi, renderApp, runtimesFixture } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";

function Location() {
  const loc = useLocation();
  return <output data-testid="location">{loc.pathname + loc.search}</output>;
}

function renderProject(route: string) {
  const project = makeProject();
  mockApi({
    ...authedRoutes,
    "GET /settings": () => ({ body: { publicHost: "", baseDomain: "test", forceHttps: false, proxy: { enabled: false, httpPort: 0, httpsPort: 0, inDocker: false, tls: false } } }),
    "GET /runtimes": () => ({ body: runtimesFixture }),
    [`GET /projects/${id}/stats`]: () => ({ body: { stats: { cpuPercent: 0, memoryBytes: 0, memoryLimit: 0, containers: [] } } }),
    [`GET /projects/${id}/share`]: () => ({ body: { share: { active: false } } }),
    [`GET /projects/${id}/plan`]: () => ({ body: { plan: { hostPath: "/data/projects/acme-shop", network: "envoryx-acme-shop", images: ["caddy:2-alpine"], containers: [] } } }),
    [`GET /projects/${id}/workers`]: () => ({ body: { workers: [], presets: [] } }),
    [`GET /projects/${id}/cron`]: () => ({ body: { jobs: [], timezone: "UTC" } }),
    [`GET /projects/${id}`]: () => ({ body: { project } }),
  });
  renderApp(
    <Routes>
      <Route
        path="/projects/:id"
        element={
          <>
            <ProjectDetailPage />
            <Location />
          </>
        }
      />
    </Routes>,
    { route },
  );
}

describe("Project navigation", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("groups the sections and keeps the chosen one in the address", async () => {
    renderProject(`/projects/${id}`);
    const user = userEvent.setup();
    const nav = await screen.findByRole("navigation", { name: "Project sections" });
    for (const group of ["Develop", "Code", "Configuration", "Data", "Observe"]) expect(within(nav).getByText(group)).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Overview" })).toHaveAttribute("aria-current", "page");

    await user.click(within(nav).getByRole("link", { name: "Environment" }));
    expect(screen.getByTestId("location")).toHaveTextContent(`/projects/${id}?tab=Environment`);
    expect(within(nav).getByRole("link", { name: "Environment" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("combobox", { name: "Project sections" })).toHaveValue("Environment");
  });

  it("shows workers and cron jobs together, also for old cron links", async () => {
    renderProject(`/projects/${id}?tab=Cron`);
    const nav = await screen.findByRole("navigation", { name: "Project sections" });
    expect(within(nav).getByRole("link", { name: "Workers & cron" })).toHaveAttribute("aria-current", "page");
    expect(await screen.findByRole("heading", { name: "Workers" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Cron jobs" })).toBeInTheDocument();
  });

  it("keeps the rarer actions in the menu and shows the Docker plan there", async () => {
    renderProject(`/projects/${id}`);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "More actions" }));
    const menu = screen.getByRole("menu");
    expect(within(menu).getAllByRole("menuitem").map((i) => i.textContent)).toEqual(["Share publicly", "Rename project", "Duplicate project", "Docker plan", "Delete project"]);
    await user.click(within(menu).getByRole("menuitem", { name: "Docker plan" }));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(await within(screen.getByRole("dialog", { name: "Docker plan" })).findByText("envoryx-acme-shop")).toBeInTheDocument();
  });
});
