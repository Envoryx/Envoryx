import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Route, Routes } from "react-router-dom";
import { ProjectDetailPage } from "./ProjectDetailPage";
import { PythonServerFields, defaultPythonServerForm, pythonCommandHint, pythonServerRequest, type PythonServerForm } from "./PythonServerFields";
import { authedRoutes, makeProject, mockApi, runtimesFixture, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const proxy = { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true };

function Harness({ initial, onChange }: { initial: Partial<PythonServerForm>; onChange: (v: PythonServerForm) => void }) {
  const [value, setValue] = useState<PythonServerForm>({ ...defaultPythonServerForm, server: true, ...initial });
  return (
    <PythonServerFields
      value={value}
      onChange={(v) => {
        setValue(v);
        onChange(v);
      }}
    />
  );
}

describe("PythonServerFields", () => {
  it("follows the preset's port and app only while untouched and shows the command", async () => {
    let last: PythonServerForm | undefined;
    render(<Harness initial={{}} onChange={(v) => (last = v)} />);
    const user = userEvent.setup();
    expect(screen.getByLabelText("Command")).toHaveValue("python manage.py runserver 0.0.0.0:8000");
    await user.selectOptions(screen.getByLabelText("Framework preset"), "flask");
    expect(screen.getByLabelText("Port inside the container")).toHaveValue(5000);
    expect(screen.getByLabelText("Application")).toHaveValue("app:app");
    expect(screen.getByLabelText("Command")).toHaveValue("flask --app app:app run --host 0.0.0.0 --port 5000 --debug");
    await user.clear(screen.getByLabelText("Application"));
    await user.type(screen.getByLabelText("Application"), "web:create_app");
    await user.selectOptions(screen.getByLabelText("Framework preset"), "wsgi");
    // The edited app stays, the port follows the preset.
    expect(screen.getByLabelText("WSGI application")).toHaveValue("web:create_app");
    expect(screen.getByLabelText("Port inside the container")).toHaveValue(8000);
    await user.click(screen.getByRole("radio", { name: "Production server" }));
    expect(screen.getByLabelText("Command")).toHaveValue("gunicorn web:create_app --bind 0.0.0.0:8000");
    await user.click(screen.getByLabelText(/Publish the debugpy port/));
    expect(screen.getByLabelText("debugpy port inside the container")).toHaveValue(5678);
    expect(pythonServerRequest(last!)).toEqual({ server: true, mode: "production", preset: "wsgi", app: "web:create_app", port: 8000, debug: true, debugPort: 5678 });
  });

  it("renders the command for every preset", () => {
    expect(pythonCommandHint("asgi", "dev", "main:app", "8000")).toBe("uvicorn main:app --host 0.0.0.0 --port 8000 --reload");
    expect(pythonCommandHint("asgi", "production", "main:app", "9000")).toBe("uvicorn main:app --host 0.0.0.0 --port 9000");
    expect(pythonCommandHint("django", "production", "config.wsgi:application", "8000")).toBe("gunicorn config.wsgi:application --bind 0.0.0.0:8000");
    expect(pythonCommandHint("module", "dev", "server", "8000")).toBe("python -m server");
  });
});

describe("Python card", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("leads the Runtime tab, shows the server link and saves server options", async () => {
    const project = makeProject({
      serves: "python",
      appService: "python",
      hostnames: ["acme-shop.test"],
      services: [
        { kind: "python", variant: "python", version: "3.13", image: "ghcr.io/envoryx/envoryx-python:3.13", enabled: true, config: { server: true, mode: "dev", preset: "asgi", app: "main:app", port: 8000, hostPort: 20001 } },
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
    // The project URL is the Python server's.
    expect((await screen.findAllByRole("link", { name: /https:\/\/acme-shop\.test/ }))[0]).toHaveAttribute("href", "https://acme-shop.test");

    await user.click(screen.getByRole("tab", { name: "Runtime" }));
    const headings = (await screen.findAllByRole("heading", { level: 2 })).map((h) => h.textContent);
    expect(headings.indexOf("Python")).toBeLessThan(headings.indexOf("Node.js"));
    expect(headings.indexOf("Python")).toBeLessThan(headings.indexOf("PHP"));
    const card = screen.getByRole("heading", { name: "Python" }).closest(".rounded-xl") as HTMLElement;
    expect(within(card).getByText("host port 20001")).toBeInTheDocument();
    expect(within(card).getByLabelText("Framework preset")).toHaveValue("asgi");
    await user.click(within(card).getByRole("radio", { name: "Production server" }));
    await user.click(within(card).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({
      python: { enabled: true, version: "3.13", server: true, mode: "production", preset: "asgi", app: "main:app", port: 8000, debug: false, debugPort: 0 },
    });
  });
});
