import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DomainsTab } from "./DomainsTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const proxy = { enabled: true, httpPort: 80, httpsPort: 8443, inDocker: true, tls: true };
const settings = { publicHost: "", baseDomain: "test", forceHttps: false, proxy };

describe("DomainsTab", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("lists domains with proxy URLs, adds and removes extra names", async () => {
    let domains = [{ hostname: "acme-shop.test", default: true }, { id: "d1", hostname: "shop.local", default: false, createdAt: "2026-09-18T10:00:00Z" }];
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings }),
      [`GET /projects/${id}/domains`]: () => ({ body: { domains, proxy } }),
      [`POST /projects/${id}/domains`]: () => {
        domains = [...domains, { id: "d2", hostname: "api.shop.local", default: false, createdAt: "2026-09-18T10:00:00Z" }];
        return { status: 201, body: { domain: domains[2] } };
      },
      [`DELETE /projects/${id}/domains/d1`]: () => {
        domains = domains.filter((d) => d.id !== "d1");
        return { status: 204 };
      },
    });
    renderApp(<DomainsTab project={makeProject({ hostnames: ["acme-shop.test", "shop.local"] })} />);
    const user = userEvent.setup();

    expect(await screen.findByText("acme-shop.test")).toBeInTheDocument();
    expect(screen.getByText("default")).toBeInTheDocument();
    // Non-standard HTTPS port shows up in links.
    expect(screen.getByRole("link", { name: /https:\/\/shop\.local:8443/ })).toHaveAttribute("href", "https://shop.local:8443");
    expect(screen.getByRole("link", { name: /http:\/\/localhost:20000/ })).toBeInTheDocument();

    await user.type(screen.getByLabelText("Add domain"), "api.shop.local");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST")).toBe(true));
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ hostname: "api.shop.local" });
    expect(await screen.findByText("api.shop.local added.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Remove shop.local" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE" && c.url.endsWith("/domains/d1"))).toBe(true));
  });

  it("shows the Vite allowed-hosts note and the dev server port for a Node-served project", async () => {
    mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings }),
      [`GET /projects/${id}/domains`]: () => ({ body: { domains: [{ hostname: "acme-shop.test", default: true }], proxy } }),
    });
    const web = makeProject().services.find((s) => s.kind === "web")!;
    const node = { kind: "node", variant: "node", version: "24", image: "ghcr.io/envoryx/envoryx-node:24", enabled: true, config: { devServer: true, preset: "vite", port: 5173, hostPort: 20010 } };
    renderApp(<DomainsTab project={makeProject({ services: [web, node], serves: "node", appService: "node" })} />);
    expect(await screen.findByText(/server\.allowedHosts of vite\.config/)).toBeInTheDocument();
    // The web container's HTTP port is unpublished behind a dev server; the node host port is the direct address.
    expect(screen.getByRole("link", { name: /http:\/\/localhost:20010/ })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /localhost:20000/ })).not.toBeInTheDocument();
  });

  it("warns when the proxy ports are not published", async () => {
    const unpublished = { ...proxy, httpPort: 0, httpsPort: 0 };
    mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: { ...settings, proxy: unpublished } }),
      [`GET /projects/${id}/domains`]: () => ({ body: { domains: [{ hostname: "acme-shop.test", default: true }], proxy: unpublished } }),
    });
    renderApp(<DomainsTab project={makeProject()} />);
    expect(await screen.findByText("Proxy ports are not published")).toBeInTheDocument();
  });
});
