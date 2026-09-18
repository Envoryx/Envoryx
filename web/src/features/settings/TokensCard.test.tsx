import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TokensCard } from "./TokensCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

describe("TokensCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("creates a token, shows the secret once with MCP config, and revokes", async () => {
    let tokens = [{ id: "t1", name: "Old", prefix: "stq_abc123", createdAt: "2026-09-18T10:00:00Z", lastUsedAt: null }];
    const api = mockApi({
      ...authedRoutes,
      "GET /tokens": () => ({ body: { tokens, mcpUrl: "https://envoryx.test/mcp" } }),
      "POST /tokens": () => {
        const token = { id: "t2", name: "Claude", prefix: "stq_zzz999", createdAt: "2026-09-18T11:00:00Z", lastUsedAt: null };
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

    await user.type(screen.getByLabelText("New token"), "Claude");
    await user.click(screen.getByRole("button", { name: "Create token" }));
    expect(await screen.findByText("stq_zzz999secretsecret")).toBeInTheDocument();
    expect(screen.getByText(/"url": "https:\/\/envoryx.test\/mcp"/)).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ name: "Claude" });

    await user.click(screen.getByRole("button", { name: "Revoke Old" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE" && c.url.endsWith("/tokens/t1"))).toBe(true));
  });
});
