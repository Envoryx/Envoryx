import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TokensCard, maxTokenScope } from "./TokensCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

describe("TokensCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("creates a token, shows the secret once with MCP config, and revokes", async () => {
    let tokens = [{ id: "t1", name: "Old", prefix: "stq_abc123", scope: "admin", projects: ["p1"], createdAt: "2026-09-18T10:00:00Z", lastUsedAt: null, expiresAt: "2026-09-25T10:00:00Z" as string | null }];
    const api = mockApi({
      ...authedRoutes,
      "GET /projects": () => ({ body: { projects: [{ id: "p1", name: "Shop" }, { id: "p2", name: "Blog" }] } }),
      "GET /tokens": () => ({ body: { tokens, mcpUrl: "https://envoryx.test/mcp" } }),
      "POST /tokens": () => {
        const token = { id: "t2", name: "Claude", prefix: "stq_zzz999", scope: "read", projects: ["p2"], createdAt: "2026-09-18T11:00:00Z", lastUsedAt: null, expiresAt: null };
        tokens = [token, ...tokens];
        return { status: 201, body: { token, secret: "stq_zzz999secretsecret", mcpUrl: "https://envoryx.test/mcp" } };
      },
      "DELETE /tokens/t1": () => {
        tokens = tokens.filter((t) => t.id !== "t1");
        return { status: 204 };
      },
    });
    renderApp(<TokensCard />);
    const user = userEvent.setup();

    expect(await screen.findByText("Old")).toBeInTheDocument();
    expect(screen.getByText(/never used/)).toBeInTheDocument();
    // Scope and project restriction of existing tokens are visible.
    expect(screen.getAllByText("Admin").length).toBeGreaterThan(1); // badge on the token + the scope option
    expect(await screen.findByText(/only Shop/)).toBeInTheDocument();
    // A token past its expiry says so.
    expect(screen.getByText("Expired")).toBeInTheDocument();
    expect(screen.getByText(/· expired /)).toBeInTheDocument();

    await user.type(screen.getByLabelText("New token"), "Claude");
    // The default scope is operate; pick read and confine to Blog.
    expect((screen.getByRole("radio", { name: /Operate/ }) as HTMLInputElement).checked).toBe(true);
    await user.click(screen.getByRole("radio", { name: /Read/ }));
    await user.click(screen.getByRole("checkbox", { name: /Limit to particular projects/ }));
    await user.click(await screen.findByRole("checkbox", { name: "Blog" }));
    // New tokens expire after 90 days unless another lifetime is picked.
    expect((screen.getByLabelText("Expires after") as HTMLSelectElement).value).toBe("90");
    await user.selectOptions(screen.getByLabelText("Expires after"), "Never");
    await user.click(screen.getByRole("button", { name: "Create token" }));
    expect(await screen.findByText("stq_zzz999secretsecret")).toBeInTheDocument();
    expect(screen.getByText(/"url": "https:\/\/envoryx.test\/mcp"/)).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ name: "Claude", scope: "read", projects: ["p2"], expiresInDays: 0 });
    expect(await screen.findByText(/never expires · never used/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Revoke Old" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE" && c.url.endsWith("/tokens/t1"))).toBe(true));
  });

  it("offers a viewer only the scopes the role allows", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /auth/me": () => ({ body: { user: { id: "u2", username: "vera", role: "viewer" } } }),
      "GET /projects": () => ({ body: { projects: [{ id: "p1", name: "Shop", access: "read" }] } }),
      "GET /tokens": () => ({ body: { tokens: [], mcpUrl: "https://envoryx.test/mcp" } }),
      "POST /tokens": () => ({ status: 201, body: { token: { id: "t2", name: "ro", prefix: "stq_x", scope: "read", projects: [], createdAt: "2026-09-18T11:00:00Z", lastUsedAt: null, expiresAt: null }, secret: "stq_xsecret", mcpUrl: "https://envoryx.test/mcp" } }),
    });
    renderApp(<TokensCard />);
    const user = userEvent.setup();
    expect(await screen.findByText("Your role allows tokens up to Read.")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Operate/ })).toBeDisabled();
    expect(screen.getByRole("radio", { name: /Admin/ })).toBeDisabled();
    expect(screen.getByRole("radio", { name: /Read/ })).toBeChecked();
    await user.type(screen.getByLabelText("New token"), "ro");
    await user.click(screen.getByRole("button", { name: "Create token" }));
    await waitFor(() => expect(api.calls.find((c) => c.method === "POST")?.body).toMatchObject({ scope: "read" }));
  });

  it("works out the highest scope from the global role and the project roles", () => {
    expect(maxTokenScope(true, "admin", [])).toBe("admin");
    expect(maxTokenScope(false, "none", [])).toBeUndefined();
    expect(maxTokenScope(false, "none", ["operate", "read"])).toBe("operate");
    expect(maxTokenScope(false, "developer", ["read"])).toBe("operate");
  });
});
