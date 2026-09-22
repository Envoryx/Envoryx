import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { ProjectDetailPage } from "./ProjectDetailPage";
import { NodeDevServerFields, defaultDevServerForm, devServerRequest, type DevServerForm } from "./NodeDevServerFields";
import { authedRoutes, makeProject, mockApi, runtimesFixture, renderApp } from "@/test/utils";
import { Route, Routes } from "react-router-dom";
import type { NodePreset } from "@/api/types";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const proxy = { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true };

/** Uncontrolled harness around the fields; exposes the current form to the test. */
function Harness({ initial, presets, onChange }: { initial: Partial<DevServerForm>; presets?: NodePreset[]; onChange: (v: DevServerForm) => void }) {
  const [value, setValue] = useState<DevServerForm>({ ...defaultDevServerForm, devServer: true, ...initial });
  const update = (v: DevServerForm) => {
    setValue(v);
    onChange(v);
  };
  return presets ? <NodeDevServerFields value={value} onChange={update} presets={presets} /> : <NodeDevServerFields value={value} onChange={update} />;
}

describe("NodeDevServerFields", () => {
  it("follows the preset's default port only while the port is untouched", async () => {
    let last: DevServerForm | undefined;
    render(<Harness initial={{}} onChange={(v) => (last = v)} />);
    const user = userEvent.setup();
    const preset = screen.getByLabelText("Framework preset");
    expect(screen.getByText("Default for Vite (Vue, React, Svelte, Laravel…): 5173")).toBeInTheDocument();

    // vite → next: 5173 is Vite's default, so the port jumps to Next's.
    await user.selectOptions(preset, "next");
    expect(last?.port).toBe("3000");
    expect(screen.getByLabelText("Port inside the container")).toHaveValue(3000);
    expect(screen.getByText("Default for Next.js: 3000")).toBeInTheDocument();

    // next → nuxt: both listen on 3000, nothing changes.
    await user.selectOptions(preset, "nuxt");
    expect(last?.port).toBe("3000");
    expect(last?.preset).toBe("nuxt");

    // A port the user typed survives every later preset change.
    await user.clear(screen.getByLabelText("Port inside the container"));
    await user.type(screen.getByLabelText("Port inside the container"), "4321");
    await user.selectOptions(preset, "vite");
    expect(last?.port).toBe("4321");
    await user.selectOptions(preset, "generic");
    expect(last?.port).toBe("4321");
  });

  it("lists the presets it is given and falls back to the built-in ones", () => {
    const { unmount } = render(<Harness initial={{ preset: "astro", port: "4321" }} presets={[{ key: "astro", label: "Astro", port: 4321 }]} onChange={() => {}} />);
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toContain("Astro");
    expect(screen.queryByRole("option", { name: /Vite/ })).not.toBeInTheDocument();
    expect(screen.getByText("Default for Astro: 4321")).toBeInTheDocument();
    unmount();

    render(<Harness initial={{}} onChange={() => {}} />);
    expect(screen.getByRole("option", { name: "Nuxt" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Vite \(Vue, React, Svelte, Laravel/ })).toBeInTheDocument();
  });

  it("describes the dev server as the application when it is primary", () => {
    render(<NodeDevServerFields value={{ ...defaultDevServerForm, devServer: true }} onChange={() => {}} primary />);
    expect(screen.getByText(/answers on the project URL; <slug>-dev\.<base domain> and a direct host port point at it too/)).toBeInTheDocument();
  });
});

describe("Node dev server", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows the dev server link and saves dev-server options", async () => {
    const project = makeProject({
      devHostname: "acme-shop-dev.test",
      services: [
        { kind: "php", variant: "php", version: "8.4", image: "ghcr.io/envoryx/envoryx-php:8.4", enabled: true, config: { memoryLimit: "256M", uploadMaxFilesize: "64M", postMaxSize: "64M", maxExecutionTime: 120, displayErrors: true, errorReporting: "E_ALL", extensions: [] } },
        { kind: "web", variant: "caddy", version: "2", image: "caddy:2-alpine", enabled: true, config: {} },
        { kind: "node", variant: "node", version: "24", image: "ghcr.io/envoryx/envoryx-node:24", enabled: true, config: { devServer: true, packageManager: "npm", script: "dev", port: 5173, preset: "vite", hostPort: 20001 } },
      ],
    });
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: { publicHost: "", baseDomain: "test", forceHttps: false, proxy } }),
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET /projects/${id}/plan`]: () => ({ body: { preview: { containers: [] } } }),
      [`GET /projects/${id}/stats`]: () => ({ body: { stats: { cpuPercent: 1, memoryBytes: 1024, memoryLimit: 2048, containers: [] } } }),
      [`GET /projects/${id}`]: () => ({ body: { project } }),
      [`PATCH /projects/${id}`]: () => ({ body: { project } }),
    });
    renderApp(
      <Routes>
        <Route path="/projects/:id" element={<ProjectDetailPage />} />
      </Routes>,
      { route: `/projects/${id}` },
    );
    const user = userEvent.setup();
    expect((await screen.findAllByRole("link", { name: /https:\/\/acme-shop-dev\.test/ }))[0]).toHaveAttribute("href", "https://acme-shop-dev.test");

    await user.click(screen.getByRole("tab", { name: "Runtime" }));
    const preset = await screen.findByLabelText("Framework preset");
    await user.selectOptions(preset, "next");
    await user.selectOptions(screen.getByLabelText("Package manager"), "pnpm");
    // The Node card's own Save button (the Python card after it has one too).
    const nodeCard = screen.getByRole("heading", { name: "Node.js" }).closest(".rounded-xl")!;
    await user.click(within(nodeCard as HTMLElement).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({
      node: { enabled: true, version: "24", devServer: true, mode: "dev", packageManager: "pnpm", script: "dev", buildScript: "", port: 3000, preset: "next", inspect: false, inspectPort: 0 },
    });
  });
});

describe("Node production mode and inspector", () => {
  it("switches the script defaults with the mode and sends build script and inspector port", async () => {
    const user = userEvent.setup();
    let latest: DevServerForm | null = null;
    function Harness() {
      const [value, setValue] = useState<DevServerForm>({ ...defaultDevServerForm, devServer: true, preset: "next", port: "3000" });
      latest = value;
      return <NodeDevServerFields value={value} onChange={setValue} />;
    }
    render(<Harness />);
    await user.click(screen.getByRole("radio", { name: "Production build" }));
    expect(screen.getByLabelText("Build script")).toHaveValue("build");
    expect(screen.getByLabelText("Serve script")).toHaveValue("start");
    await user.selectOptions(screen.getByLabelText("Framework preset"), "vite");
    expect(screen.getByLabelText("Serve script")).toHaveValue("preview");
    await user.clear(screen.getByLabelText("Serve script"));
    await user.type(screen.getByLabelText("Serve script"), "serve");
    await user.click(screen.getByRole("radio", { name: "Dev server" }));
    // A hand-edited script is kept when the mode changes.
    expect(screen.getByLabelText("Script")).toHaveValue("serve");
    await user.click(screen.getByRole("checkbox", { name: /Publish the Node.js inspector port/ }));
    expect(screen.getByLabelText("Inspector port inside the container")).toHaveValue(9229);
    expect(devServerRequest(latest!)).toEqual({ devServer: true, mode: "dev", packageManager: "npm", script: "serve", buildScript: "", port: 5173, preset: "vite", inspect: true, inspectPort: 9229 });
  });
});

describe("PHP card", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("adds PHP to a project without it and removes it again", async () => {
    const project = makeProject({
      serves: "static",
      services: [{ kind: "web", variant: "caddy", version: "2", image: "caddy:2-alpine", enabled: true, config: {} }],
      status: { state: "running", services: [], warnings: [] },
    });
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: { publicHost: "", baseDomain: "test", forceHttps: false, proxy } }),
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET /projects/${id}/plan`]: () => ({ body: { preview: { containers: [] } } }),
      [`GET /projects/${id}/stats`]: () => ({ body: { stats: { cpuPercent: 1, memoryBytes: 1024, memoryLimit: 2048, containers: [] } } }),
      [`GET /projects/${id}`]: () => ({ body: { project } }),
      [`PATCH /projects/${id}`]: () => ({ body: { project } }),
    });
    renderApp(
      <Routes>
        <Route path="/projects/:id" element={<ProjectDetailPage />} />
      </Routes>,
      { route: `/projects/${id}` },
    );
    const user = userEvent.setup();
    await user.click(await screen.findByRole("tab", { name: "Runtime" }));
    const php = await screen.findByRole("checkbox", { name: /Enable PHP/ });
    expect(php).not.toBeChecked();
    await user.click(php);
    expect(screen.getByLabelText("PHP version")).toHaveValue("8.4");
    // Only the PHP card has unsaved changes, so it owns the one enabled Save button.
    await user.click(screen.getAllByRole("button", { name: "Save" }).find((b) => !b.hasAttribute("disabled"))!);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    const body = api.calls.find((c) => c.method === "PATCH")!.body as { php: { enabled: boolean; version: string; config: unknown } };
    expect(body.php.enabled).toBe(true);
    expect(body.php.version).toBe("8.4");
    expect(body.php.config).toEqual(runtimesFixture.phpDefaults);
  });
});
