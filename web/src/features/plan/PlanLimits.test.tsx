import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { NewProjectButton } from "@/features/projects/NewProjectButton";
import { ProjectDetailPage } from "@/features/projects/ProjectDetailPage";
import { UsersCard } from "@/features/settings/UsersCard";
import { authedRoutes, makeProject, mockApi, runtimesFixture, renderApp } from "@/test/utils";

const plan = { name: "Starter", limits: { projects: 2, users: 2 }, runtimes: ["php", "node"], disabled: [], lockedSettings: [] };
const full = { "GET /plan": () => ({ body: { plan, usage: { projects: 2, users: 2, diskBytes: 0 } } }) };

describe("the UI at the plan's limits", () => {
  it("disables New project and Invite user with a hint", async () => {
    mockApi({ ...authedRoutes, ...full, "GET /projects": () => ({ body: { projects: [] } }), "GET /users": () => ({ body: { users: [] } }) });
    renderApp(
      <>
        <NewProjectButton />
        <UsersCard />
      </>,
    );
    expect(await screen.findByText("The plan's project limit is reached.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New project" })).toBeDisabled();
    expect(await screen.findByText("The plan's user limit is reached.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Invite user" })).toBeDisabled();
  });

  it("keeps New project a link below the limit", async () => {
    mockApi({ ...authedRoutes, "GET /plan": () => ({ body: { plan, usage: { projects: 1, users: 1, diskBytes: 0 } } }) });
    renderApp(<NewProjectButton />);
    expect(await screen.findByRole("link", { name: "New project" })).toHaveAttribute("href", "/projects/new");
  });

  it("offers only the runtimes the plan includes, and those the project has", async () => {
    const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
    const project = makeProject({
      serves: "go",
      appService: "go",
      services: [
        { kind: "go", variant: "go", version: "1.27", image: "ghcr.io/envoryx/envoryx-go:1.27", enabled: true, config: {} },
        { kind: "web", variant: "caddy", version: "2", image: "caddy:2-alpine", enabled: true, config: {} },
      ],
    });
    mockApi({
      ...authedRoutes,
      ...full,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET /projects/${id}/plan`]: () => ({ body: { preview: { containers: [] } } }),
      [`GET /projects/${id}/stats`]: () => ({ body: { stats: { cpuPercent: 1, memoryBytes: 1024, memoryLimit: 2048, containers: [] } } }),
      [`GET /projects/${id}`]: () => ({ body: { project } }),
    });
    renderApp(
      <Routes>
        <Route path="/projects/:id" element={<ProjectDetailPage />} />
      </Routes>,
      { route: `/projects/${id}` },
    );
    const user = userEvent.setup();
    await user.click(await screen.findByRole("link", { name: "Runtime" }));
    expect(await screen.findByRole("heading", { name: "Node.js" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "PHP" })).toBeInTheDocument();
    // Go is the project's, though the plan leaves it out; Python and the others aren't offered.
    expect(screen.getByRole("heading", { name: "Go" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Python" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: ".NET" })).not.toBeInTheDocument();
    expect(screen.getByText("More runtimes aren't included in your plan. Your hoster can include them.")).toBeInTheDocument();
  });
});
