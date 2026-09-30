import { screen } from "@testing-library/react";
import { Route, Routes } from "react-router-dom";
import { RequireAdmin } from "./App";
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
