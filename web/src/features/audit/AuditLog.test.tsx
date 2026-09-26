import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuditLog } from "./AuditLog";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const updated = {
  id: "e1",
  createdAt: "2026-09-26T10:00:00Z",
  username: "admin (token: ci)",
  action: "project.updated",
  targetType: "project",
  targetId: "p1",
  ip: "10.0.0.2",
  details: { name: "Shop", changes: { php: "8.4" }, diff: [{ section: "php", from: "version: 8.3", to: "version: 8.4" }, { section: "env", item: "APP_ENV", from: "local", to: "production" }] },
};
const login = { id: "e2", createdAt: "2026-09-26T09:00:00Z", username: "dev", action: "auth.login", targetType: "user", targetId: "u2", ip: "10.0.0.3", details: {} };
const older = { id: "e3", createdAt: "2026-09-25T09:00:00Z", username: "", action: "backup.created", targetType: "project", targetId: "p1", ip: "", details: { name: "Shop" } };

describe("AuditLog", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("filters, pages, expands a change and exports with the filters", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /audit/users": () => ({ body: { users: ["admin", "dev"] } }),
      "GET /audit?": (url) => ({ body: url.includes("after=") ? { entries: [older], next: "" } : { entries: [updated, login], next: "c1" } }),
    });
    renderApp(<AuditLog />);
    const user = userEvent.setup();

    expect(await screen.findByText("admin (token: ci)")).toBeInTheDocument();
    expect(screen.getByText("(2 settings changed)")).toBeInTheDocument();

    // The change, opened.
    await user.click(screen.getAllByRole("button", { name: "Show details" })[0]!);
    expect(screen.getByText("version: 8.3")).toBeInTheDocument();
    expect(screen.getByText("production")).toBeInTheDocument();

    // Older entries.
    await user.click(screen.getByRole("button", { name: "Load older entries" }));
    expect(await screen.findByText("Envoryx (automatic)")).toBeInTheDocument();
    expect(api.calls.some((c) => c.url.includes("after=c1"))).toBe(true);

    // Filters reach the server and the export links.
    await user.selectOptions(screen.getByLabelText("Category"), "projects");
    await user.selectOptions(screen.getByLabelText("User"), "admin");
    await user.type(screen.getByLabelText("From"), "2026-09-01");
    await waitFor(() => expect(api.calls.some((c) => c.url.includes("action=project.") && c.url.includes("user=admin") && c.url.includes("since=2026-09-01"))).toBe(true));
    const csv = screen.getByRole("link", { name: /CSV/ });
    expect(csv.getAttribute("href")).toContain("/api/v1/audit/export?");
    expect(csv.getAttribute("href")).toContain("action=project.");
    expect(csv.getAttribute("href")).toContain("format=csv");
  });

  it("filters by a user when their name is clicked and keeps a project fixed", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /audit/users": () => ({ body: { users: ["admin", "dev"] } }),
      "GET /audit?": () => ({ body: { entries: [updated, login], next: "" } }),
    });
    renderApp(<AuditLog project="p1" />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "admin (token: ci)" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.includes("user=admin") && c.url.includes("project=p1"))).toBe(true));
    expect(screen.getByLabelText("User")).toHaveValue("admin");
    expect(api.calls.filter((c) => c.url.startsWith("/api/v1/audit?")).every((c) => c.url.includes("project=p1"))).toBe(true);
  });
});
