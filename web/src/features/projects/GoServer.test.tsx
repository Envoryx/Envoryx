import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Route, Routes } from "react-router-dom";
import { ProjectDetailPage } from "./ProjectDetailPage";
import { GoServerFields, defaultGoServerForm, goCommandHint, goServerRequest, type GoServerForm } from "./GoServerFields";
import { authedRoutes, makeProject, mockApi, runtimesFixture, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const proxy = { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true };

function Harness({ onChange }: { onChange: (v: GoServerForm) => void }) {
  const [value, setValue] = useState<GoServerForm>({ ...defaultGoServerForm, server: true });
  return (
    <GoServerFields
      value={value}
      onChange={(v) => {
        setValue(v);
        onChange(v);
      }}
    />
  );
}

describe("GoServerFields", () => {
  it("shows the command for the mode and turns the form into the request", async () => {
    let last: GoServerForm | undefined;
    render(<Harness onChange={(v) => (last = v)} />);
    const user = userEvent.setup();
    expect(screen.getByLabelText("Command")).toHaveValue("air (go build .)");
    await user.clear(screen.getByLabelText("Main package"));
    await user.type(screen.getByLabelText("Main package"), "./cmd/server");
    await user.click(screen.getByRole("radio", { name: "Production build" }));
    expect(screen.getByLabelText("Command")).toHaveValue("go build ./cmd/server && ./app");
    await user.click(screen.getByLabelText(/Debug with Delve/));
    expect(screen.getByLabelText("Delve port inside the container")).toHaveValue(2345);
    expect(screen.getByLabelText("Command")).toHaveValue("go build -gcflags='all=-N -l' ./cmd/server && dlv exec --headless …");
    expect(goServerRequest(last!)).toEqual({ server: true, mode: "production", package: "./cmd/server", port: 8080, debug: true, debugPort: 2345 });
    expect(goCommandHint("dev", "", true)).toBe("air (go build -gcflags='all=-N -l' ., run under dlv)");
  });
});

describe("Go card", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("leads the Runtime tab, shows the server and Delve ports and saves", async () => {
    const project = makeProject({
      serves: "go",
      appService: "go",
      hostnames: ["acme-shop.test"],
      services: [
        { kind: "go", variant: "go", version: "1.27", image: "ghcr.io/envoryx/envoryx-go:1.27", enabled: true, config: { server: true, mode: "dev", package: "./cmd/server", port: 8080, hostPort: 20001, debug: true, debugPort: 2345, debugHostPort: 20002 } },
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

    await user.click(screen.getByRole("tab", { name: "Runtime" }));
    const headings = (await screen.findAllByRole("heading", { level: 2 })).map((h) => h.textContent);
    expect(headings.indexOf("Go")).toBeLessThan(headings.indexOf("PHP"));
    const card = screen.getByRole("heading", { name: "Go" }).closest(".rounded-xl") as HTMLElement;
    expect(within(card).getByText("host port 20001")).toBeInTheDocument();
    expect(within(card).getByText("Delve on host port 20002")).toBeInTheDocument();
    expect(within(card).getByLabelText("Main package")).toHaveValue("./cmd/server");
    await user.click(within(card).getByRole("radio", { name: "Production build" }));
    await user.click(within(card).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({
      go: { enabled: true, version: "1.27", server: true, mode: "production", package: "./cmd/server", port: 8080, debug: true, debugPort: 2345 },
    });
  });
});
