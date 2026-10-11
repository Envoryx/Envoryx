import { screen } from "@testing-library/react";
import { Route, Routes } from "react-router-dom";
import { RequireAdmin, Unsuspended } from "./App";
import { mockApi, renderApp } from "@/test/utils";

function routes() {
  return (
    <Routes>
      <Route
        path="/docker"
        element={
          <RequireAdmin>
            <h1>Docker</h1>
          </RequireAdmin>
        }
      />
      <Route path="/projects" element={<h1>Projects</h1>} />
    </Routes>
  );
}

const me = (body: unknown) => ({ "GET /setup": () => ({ body: { needsSetup: false } }), "GET /auth/me": () => ({ body }) });

describe("RequireAdmin", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("lets an instance admin in", async () => {
    mockApi(me({ user: { id: "u1", username: "admin", role: "admin" }, admin: true, projectRoles: {} }));
    renderApp(routes(), { route: "/docker" });
    expect(await screen.findByRole("heading", { name: "Docker" })).toBeInTheDocument();
  });

  it("sends a developer to the project list", async () => {
    mockApi(me({ user: { id: "u2", username: "dev", role: "developer" }, admin: false, projectRoles: {} }));
    renderApp(routes(), { route: "/docker" });
    expect(await screen.findByRole("heading", { name: "Projects" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Docker" })).not.toBeInTheDocument();
  });

  it("follows the server's admin flag over the role, as for a token with less scope than its owner", async () => {
    mockApi(me({ user: { id: "u1", username: "admin", role: "admin" }, admin: false, projectRoles: {}, token: { name: "ci", scope: "operate", projects: [] } }));
    renderApp(routes(), { route: "/docker" });
    expect(await screen.findByRole("heading", { name: "Projects" })).toBeInTheDocument();
  });
});

describe("Unsuspended", () => {
  afterEach(() => vi.unstubAllGlobals());

  const page = (
    <Unsuspended>
      <h1>Dashboard</h1>
    </Unsuspended>
  );

  it("shows the hoster's message instead of the pages of a suspended instance", async () => {
    mockApi({
      ...me({ user: { id: "u1", username: "admin", role: "admin" }, admin: true }),
      "GET /plan": () => ({ body: { plan: null, suspension: { since: "2026-10-11T08:00:00Z", message: "Invoice 1234 is open" } } }),
    });
    renderApp(page);
    expect(await screen.findByRole("heading", { name: "This instance is suspended" })).toBeInTheDocument();
    expect(screen.getByText("Invoice 1234 is open")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Dashboard" })).not.toBeInTheDocument();
  });

  it("shows the pages otherwise", async () => {
    mockApi({ ...me({ user: { id: "u1", username: "admin", role: "admin" }, admin: true }), "GET /plan": () => ({ body: { plan: null } }) });
    renderApp(page);
    expect(await screen.findByRole("heading", { name: "Dashboard" })).toBeInTheDocument();
  });
});
