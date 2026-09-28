import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { UsersCard } from "./UsersCard";
import { OidcCard } from "./OidcCard";
import { SettingsPage } from "./SettingsPage";
import { InvitePage } from "@/features/auth/InvitePage";
import { LoginPage } from "@/features/auth/LoginPage";
import { ProjectDetailPage } from "@/features/projects/ProjectDetailPage";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const shop = makeProject();
const dana = { id: "u2", username: "dana", role: "viewer", disabled: false, invited: false, sso: false, projectRoles: { [shop.id]: "developer" }, createdAt: "2026-09-28T10:00:00Z" };
const viewerMe = { "GET /setup": () => ({ body: { needsSetup: false } }), "GET /auth/me": () => ({ body: { user: { id: "u2", username: "dana", role: "viewer" } } }) };

describe("UsersCard", () => {
  it("invites a user, shows the link once and changes roles", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /projects": () => ({ body: { projects: [shop] } }),
      "GET /users": () => ({ body: { users: [{ ...dana, id: "u1", username: "admin", role: "admin", projectRoles: {} }, dana] } }),
      "POST /users": () => ({ status: 201, body: { user: { ...dana, id: "u3", username: "eve", invited: true }, inviteUrl: "http://envoryx.test/invite/abc", expiresAt: "2026-09-30T10:00:00Z" } }),
      "PATCH /users/u2": () => ({ body: { user: { ...dana, role: "developer" } } }),
      "PUT /users/u2/projects": () => ({ body: { user: dana } }),
    });
    renderApp(<UsersCard />);
    const user = userEvent.setup();
    const row = (await screen.findByText("dana")).closest("li") as HTMLElement;
    expect(within(row).getByText("1 project role")).toBeInTheDocument();
    // You cannot disable or delete yourself.
    const me = screen.getByText("you").closest("li") as HTMLElement;
    expect(within(me).queryByRole("button", { name: "Disable" })).not.toBeInTheDocument();

    await user.selectOptions(within(row).getByLabelText("Role of dana"), "developer");
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({ role: "developer" });

    await user.click(within(row).getByRole("button", { name: "Projects" }));
    await user.selectOptions(await within(row).findByLabelText("Role of dana in Acme Shop"), "");
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({ role: "" });

    await user.click(screen.getByRole("button", { name: "Invite user" }));
    await user.type(screen.getByLabelText("Username"), "eve");
    await user.click(screen.getByRole("button", { name: "Create invitation" }));
    expect(await screen.findByText("http://envoryx.test/invite/abc")).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ username: "eve", role: "developer" });
  });
});

describe("OidcCard", () => {
  it("shows the redirect URL, keeps the secret out and saves group names as lists", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings/oidc": () => ({ body: { oidc: { enabled: false, name: "SSO", issuer: "", clientId: "", defaultRole: "deny", autoCreate: false, hasSecret: true, redirectUrl: "https://envoryx.lan/api/v1/auth/oidc/callback" } } }),
      "PUT /settings/oidc": (_u, init) => ({ body: { oidc: JSON.parse(init.body as string) } }),
    });
    renderApp(<OidcCard />);
    const user = userEvent.setup();
    expect(await screen.findByText("https://envoryx.lan/api/v1/auth/oidc/callback")).toBeInTheDocument();
    expect(screen.getByText("Stored; leave empty to keep it.")).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: /Offer single sign-on/ }));
    await user.type(screen.getByLabelText("Issuer URL"), "https://auth.lan/application/o/envoryx/");
    await user.type(screen.getByLabelText("Client ID"), "envoryx");
    await user.type(screen.getByLabelText("Admin groups"), "ops, admins");
    await user.selectOptions(screen.getByLabelText("Users in none of these groups"), "viewer");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toMatchObject({ enabled: true, issuer: "https://auth.lan/application/o/envoryx/", clientId: "envoryx", clientSecret: "", adminGroups: ["ops", "admins"], defaultRole: "viewer" });
  });
});

describe("Invitations and sign-in", () => {
  it("sets the password through an invitation link", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    const api = mockApi({
      "GET /auth/oidc": () => ({ body: { enabled: false } }),
      "GET /invites/tok123": () => ({ body: { username: "dana", expiresAt: "2026-09-30T10:00:00Z", reset: false } }),
      "POST /invites/tok123": () => ({ body: { user: { id: "u2", username: "dana", role: "viewer" } } }),
    });
    renderApp(
      <Routes>
        <Route path="/invite/:token" element={<InvitePage />} />
      </Routes>,
      { route: "/invite/tok123" },
    );
    const user = userEvent.setup();
    expect(await screen.findByText("Choose the password for dana.")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Password"), "dana-secret-123");
    await user.type(screen.getByLabelText("Confirm password"), "dana-secret-123");
    await user.click(screen.getByRole("button", { name: "Create account" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/"));
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ password: "dana-secret-123" });
    vi.unstubAllGlobals();
  });

  it("offers single sign-on on the login page and shows why it failed", async () => {
    mockApi({
      "GET /setup": () => ({ body: { needsSetup: false } }),
      "GET /auth/me": () => ({ status: 401, body: { error: { code: "unauthorized", message: "unauthenticated" } } }),
      "GET /auth/oidc": () => ({ body: { enabled: true, name: "Authentik" } }),
    });
    renderApp(
      <Routes>
        <Route path="/login" element={<LoginPage mode="login" />} />
      </Routes>,
      { route: "/login?sso_error=there%20is%20no%20Envoryx%20account%20for%20eve" },
    );
    const link = await screen.findByRole("link", { name: "Sign in with Authentik" });
    expect(link).toHaveAttribute("href", "/api/v1/auth/oidc/start?return=%2F");
    expect(screen.getByText("there is no Envoryx account for eve")).toBeInTheDocument();
  });
});

describe("Roles in the UI", () => {
  it("shows a user who is not an admin only their account settings", async () => {
    mockApi({
      ...viewerMe,
      "GET /tokens": () => ({ body: { tokens: [], mcpUrl: "http://x/mcp" } }),
      "GET /auth/ssh-keys": () => ({ body: { keys: "" } }),
    });
    renderApp(<SettingsPage />);
    expect(await screen.findByText("My SSH keys")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Account" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Users" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Diagnostics" })).not.toBeInTheDocument();
  });

  it("lets a viewer look at a project but not start, rename or delete it", async () => {
    const project = { ...shop, access: "read" as const };
    mockApi({
      ...viewerMe,
      "GET /settings": () => ({ body: { publicHost: "", baseDomain: "test", forceHttps: false, proxy: { enabled: false, httpPort: 0, httpsPort: 0, inDocker: false, tls: false } } }),
      "GET /runtimes": () => ({ body: { runtimes: [], templates: [], phpExtensions: [] } }),
      [`GET /projects/${shop.id}/plan`]: () => ({ body: { preview: { containers: [] } } }),
      [`GET /projects/${shop.id}/stats`]: () => ({ body: { stats: { cpuPercent: 0, memoryBytes: 0, memoryLimit: 0, containers: [] } } }),
      [`GET /projects/${shop.id}`]: () => ({ body: { project } }),
    });
    renderApp(
      <Routes>
        <Route path="/projects/:id" element={<ProjectDetailPage />} />
      </Routes>,
      { route: `/projects/${shop.id}` },
    );
    expect(await screen.findByRole("heading", { name: /Acme Shop/ })).toBeInTheDocument();
    for (const name of ["Start", "Stop", "Restart - also pulls updated runtime images"]) {
      const b = screen.queryByTitle(name);
      if (b) expect(b).toBeDisabled();
    }
    expect(screen.queryByRole("button", { name: "Delete project" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rename project" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Terminal" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "History" })).not.toBeInTheDocument();
  });
});
