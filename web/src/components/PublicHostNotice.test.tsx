import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PublicHostNotice } from "./PublicHostNotice";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const settings = (extra: Record<string, unknown>) => ({
  publicHost: "",
  warnings: [],
  baseDomain: "test",
  forceHttps: false,
  proxy: { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true, address: "192.168.1.5" },
  version: "test",
  schemaVersion: 8,
  ...extra,
});

describe("PublicHostNotice", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("stays silent when no public host is needed", async () => {
    mockApi({ ...authedRoutes, "GET /settings": () => ({ body: settings({ publicHostNeeded: false }) }) });
    renderApp(<PublicHostNotice />);
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText(/Project links need a host/)).not.toBeInTheDocument();
  });

  it("offers the Docker host the daemon reports and saves it", async () => {
    let saved: unknown;
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings({ publicHostNeeded: true, publicHostSuggestion: { hostname: "Tower", ip: "192.168.1.24" } }) }),
      "PATCH /settings": (_url, init) => {
        saved = JSON.parse(init.body as string);
        return { body: settings({ publicHost: "192.168.1.24", publicHostNeeded: false }) };
      },
    });
    renderApp(<PublicHostNotice />);
    expect(await screen.findByText(/Project links need a host/)).toBeInTheDocument();
    expect(screen.getByText(/192\.168\.1\.5/)).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Use 192.168.1.24" }));
    await waitFor(() => expect(saved).toEqual({ publicHost: "192.168.1.24" }));
    await waitFor(() => expect(screen.queryByText(/Project links need a host/)).not.toBeInTheDocument());
    expect(api.calls.some((c) => c.method === "PATCH")).toBe(true);
  });
});
