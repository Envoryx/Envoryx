import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SettingsPage } from "./SettingsPage";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const settings = { publicHost: "", warnings: [], baseDomain: "test", forceHttps: false, proxy: { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true, address: "192.168.1.5" }, version: "test", schemaVersion: 8, configDir: "/config", projectsDir: "/projects", hostPath: { overrides: {}, detected: {}, bareMetal: true, selfContainerId: "" }, portRange: { start: 20000, end: 20999 }, puid: 99, pgid: 100, dockerHost: "", session: { idleTimeout: "12h", absoluteTimeout: "168h" }, secureCookies: false };

describe("Settings diagnostics", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("lists checks by group, counts problems on the tab and applies a one-click fix", async () => {
    let publicHost = "";
    const checks = () => [
      { id: "docker.engine", category: "runtime", status: "ok", title: "Docker engine", detail: "Docker 29 on Linux" },
      publicHost
        ? { id: "network.publicHost", category: "network", status: "ok", title: "Host for project links", detail: "Links use " + publicHost }
        : { id: "network.publicHost", category: "network", status: "warning", title: "Host for project links", detail: "Envoryx has its own IP", hint: "Set the Docker host's address under Settings → General → Host for project links.", action: { kind: "setPublicHost", value: "192.168.1.24" }, docs: "project-links" },
      { id: "maintenance.notifications", category: "maintenance", status: "info", title: "Notifications", detail: "No channel configured.", hint: "Configure a channel.", action: { kind: "settingsTab", value: "notifications", label: "Configure" } },
    ];
    let probeReachable = true;
    const api = mockApi({
      ...authedRoutes,
      "GET http://envoryx-diagnostics-probe.test/": () => (probeReachable ? { body: { envoryx: "probe", host: "envoryx-diagnostics-probe.test", tls: false } } : { status: 404, body: {} }),
      // Routes match by prefix: the more specific one first.
      "GET /settings/notifications": () => ({ body: { status: { config: { enabled: false, provider: "ntfy", events: [] }, hasToken: false, hasSmtpPassword: false }, providers: { ntfy: "ntfy" }, kinds: [] } }),
      "GET /settings": () => ({ body: { ...settings, publicHost } }),
      "PATCH /settings": (_url, init) => {
        publicHost = (JSON.parse(init.body as string) as { publicHost: string }).publicHost;
        return { body: { ...settings, publicHost } };
      },
      "GET /system/diagnostics": () => {
        const list = checks();
        const summary = { ok: 0, info: 0, warning: 0, error: 0 };
        for (const c of list) summary[c.status as keyof typeof summary]++;
        return { body: { checks: list, summary, at: "2026-09-19T20:00:00Z" } };
      },
    });
    renderApp(<SettingsPage />);
    const user = userEvent.setup();

    // Diagnostics is the first tab: groups, the warning with its hint and the fix button.
    expect(await screen.findByText("1 warning")).toBeInTheDocument();
    expect(screen.getByText("Docker & storage")).toBeInTheDocument();
    expect(screen.getByText("Network & links")).toBeInTheDocument();
    expect(screen.getByText(/Set the Docker host's address/)).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /Diagnostics/ })).toHaveTextContent("1");

    await user.click(screen.getByRole("button", { name: "Use 192.168.1.24" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH" && c.url.endsWith("/settings"))).toBe(true));
    await waitFor(() => expect(screen.getByText("Everything looks good")).toBeInTheDocument());
    expect(screen.getByRole("tab", { name: /Diagnostics/ })).toHaveTextContent("✓");

    // The browser probe is part of the network group and passed.
    expect(screen.getByText("Project domains from this browser")).toBeInTheDocument();
    expect(await screen.findByText(/Wildcard DNS and the proxy work from this device/)).toBeInTheDocument();

    // When the probe fails, the summary counts it and explains the DNS side.
    probeReachable = false;
    await user.click(screen.getByRole("button", { name: "Check again" }));
    expect(await screen.findByText(/could not be reached from this browser/)).toBeInTheDocument();
    expect(screen.getByText("1 warning")).toBeInTheDocument();
    expect(screen.getByText(/This device's DNS does not resolve names under the base domain/)).toBeInTheDocument();

    // An action of kind settingsTab switches the tab.
    await user.click(screen.getByRole("button", { name: "Configure" }));
    await waitFor(() => expect(screen.getByRole("tab", { name: "Notifications" })).toHaveAttribute("aria-selected", "true"));
  });
});
