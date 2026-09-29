import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Route, Routes } from "react-router-dom";
import { ProjectDetailPage } from "./ProjectDetailPage";
import { IdeTab } from "./IdeTab";
import { DotnetServerFields, defaultDotnetServerForm, dotnetCommandHint, dotnetServerRequest, type DotnetServerForm } from "./DotnetServerFields";
import { authedRoutes, makeProject, mockApi, runtimesFixture, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const proxy = { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true };

function Harness({ onChange }: { onChange: (v: DotnetServerForm) => void }) {
  const [value, setValue] = useState<DotnetServerForm>({ ...defaultDotnetServerForm, server: true });
  return (
    <DotnetServerFields
      value={value}
      onChange={(v) => {
        setValue(v);
        onChange(v);
      }}
    />
  );
}

describe("DotnetServerFields", () => {
  it("shows the command per preset and mode, asks for a DLL only for the dll preset and builds the request", async () => {
    let last: DotnetServerForm | undefined;
    render(<Harness onChange={(v) => (last = v)} />);
    const user = userEvent.setup();
    expect(screen.getByLabelText("Command")).toHaveValue("dotnet watch --project <project>.csproj run");
    expect(screen.queryByLabelText("DLL")).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("Project file"), "src/Shop/Shop.csproj");
    expect(screen.getByLabelText("Command")).toHaveValue("dotnet watch --project src/Shop/Shop.csproj run");
    await user.click(screen.getByRole("radio", { name: "Production server" }));
    expect(screen.getByLabelText("Command")).toHaveValue("dotnet publish src/Shop/Shop.csproj, then dotnet bin/envoryx-publish/<app>.dll");

    // The dll preset has no dev mode of its own, so the mode choice goes away.
    await user.selectOptions(screen.getByLabelText("Framework preset"), "dll");
    expect(screen.queryByRole("radio", { name: "Production server" })).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("DLL"), "bin/envoryx-publish/Worker.dll");
    expect(dotnetServerRequest(last!)).toEqual({ server: true, mode: "production", preset: "dll", project: "src/Shop/Shop.csproj", dll: "bin/envoryx-publish/Worker.dll", port: 8080 });
    // Leaving the dll preset drops the DLL from the request.
    expect(dotnetServerRequest({ ...last!, preset: "aspnetcore" }).dll).toBe("");
    expect(dotnetCommandHint("dll", "dev", "", "")).toBe("dotnet publish <project>.csproj, then dotnet bin/envoryx-publish/<app>.dll");
  });
});

describe(".NET card", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("leads the Runtime tab, shows the server port and saves", async () => {
    const project = makeProject({
      serves: "dotnet",
      appService: "dotnet",
      hostnames: ["acme-shop.test"],
      services: [
        { kind: "dotnet", variant: "dotnet", version: "10", image: "ghcr.io/envoryx/envoryx-dotnet:10", enabled: true, config: { server: true, mode: "dev", preset: "aspnetcore", port: 8080, hostPort: 20001 } },
        { kind: "web", variant: "caddy", version: "2", image: "caddy:2-alpine", enabled: true, config: {} },
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
    expect((await screen.findAllByRole("link", { name: /https:\/\/acme-shop\.test/ }))[0]).toHaveAttribute("href", "https://acme-shop.test");

    await user.click(screen.getByRole("link", { name: "Runtime" }));
    const headings = (await screen.findAllByRole("heading", { level: 2 })).map((h) => h.textContent);
    expect(headings.indexOf(".NET")).toBeLessThan(headings.indexOf("PHP"));
    const card = screen.getByRole("heading", { name: ".NET" }).closest(".rounded-xl") as HTMLElement;
    expect(within(card).getByText("host port 20001")).toBeInTheDocument();
    expect(within(card).getByLabelText("Framework preset")).toHaveValue("aspnetcore");
    await user.selectOptions(within(card).getByLabelText(".NET version"), "8");
    await user.click(within(card).getByRole("radio", { name: "Production server" }));
    await user.click(within(card).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({
      dotnet: { enabled: true, version: "8", server: true, mode: "production", preset: "aspnetcore", project: "", dll: "", port: 8080 },
    });
  });
});

describe("IDE tab for .NET", () => {
  it("names the .NET SSH user and shows the netcoredbg launch configuration", async () => {
    const project = makeProject({
      slug: "acme-api",
      serves: "dotnet",
      appService: "dotnet",
      services: [
        { kind: "dotnet", variant: "dotnet", version: "10", image: "ghcr.io/envoryx/envoryx-dotnet:10", enabled: true, config: { server: true, hostPort: 20001 } },
        { kind: "node", variant: "node", version: "22", image: "ghcr.io/envoryx/envoryx-node:22", enabled: true, config: {} },
        { kind: "web", variant: "caddy", version: "2", image: "caddy:2-alpine", enabled: true, config: {} },
      ],
    });
    mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: { publicHost: "192.168.1.10", baseDomain: "test", forceHttps: false, proxy: { ...proxy, address: "192.168.1.10" }, ssh: { enabled: true, port: 2222, fingerprint: "SHA256:abc", fingerprintMd5: "MD5:a4:14" }, projectsDir: "/projects", hostPath: { overrides: {}, detected: {}, bareMetal: false } } }),
      [`GET /projects/${id}/extras`]: () => ({ body: { services: [] } }),
    });
    renderApp(<IdeTab project={project} />);
    expect(await screen.findByText(".NET debugging (netcoredbg)")).toBeInTheDocument();
    expect(screen.getAllByText("acme-api.dotnet").length).toBeGreaterThan(0);
    // Once the settings are in, the pipe connects to the SSH address as the .NET user.
    const launch = await screen.findByText(/"acme-api\.dotnet@192\.168\.1\.10"/);
    expect(launch.textContent).toContain('"pipeProgram": "ssh"');
    expect(launch.textContent).toContain('"debuggerPath": "/usr/local/bin/netcoredbg"');
  });
});
