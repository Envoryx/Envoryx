import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PackageCacheCard } from "./PackageCacheCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

describe("PackageCacheCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows what each tool keeps and empties one or all", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /package-cache": () => ({ body: { cache: { path: "/config/cache", bytes: 3_000_000, entries: [{ tool: "npm", bytes: 2_000_000 }, { tool: "composer", bytes: 1_000_000 }] } } }),
      "DELETE /package-cache?tool=npm": () => ({ body: { cache: { path: "/config/cache", bytes: 1_000_000, entries: [{ tool: "composer", bytes: 1_000_000 }, { tool: "npm", bytes: 0 }] } } }),
      "DELETE /package-cache": () => ({ body: { cache: { path: "/config/cache", bytes: 0, entries: [] } } }),
    });
    renderApp(<PackageCacheCard />);
    const user = userEvent.setup();
    expect(await screen.findByText("npm")).toBeInTheDocument();
    expect(screen.getByText("Composer")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Empty the npm cache" }));
    expect(await screen.findByText(/npm cache emptied/)).toBeInTheDocument();
    expect(api.calls.some((c) => c.method === "DELETE" && c.url.endsWith("/package-cache?tool=npm"))).toBe(true);
    await user.click(screen.getByRole("button", { name: "Empty the whole cache" }));
    await waitFor(() => expect(screen.getByText(/Package cache emptied/)).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Empty the whole cache" })).toBeDisabled();
  });
});
