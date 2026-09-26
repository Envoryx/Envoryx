import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Route, Routes } from "react-router-dom";
import { ProjectDetailPage } from "./ProjectDetailPage";
import { RubyServerFields, defaultRubyServerForm, rubyCommandHint, rubyServerRequest, type RubyServerForm } from "./RubyServerFields";
import { authedRoutes, makeProject, mockApi, runtimesFixture, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const proxy = { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true };

function Harness({ onChange }: { onChange: (v: RubyServerForm) => void }) {
  const [value, setValue] = useState<RubyServerForm>({ ...defaultRubyServerForm, server: true });
  return (
    <RubyServerFields
      value={value}
      onChange={(v) => {
        setValue(v);
        onChange(v);
      }}
    />
  );
}

describe("RubyServerFields", () => {
  it("follows the preset's port, shows the command and turns the form into the request", async () => {
    let last: RubyServerForm | undefined;
    render(<Harness onChange={(v) => (last = v)} />);
    const user = userEvent.setup();
    expect(screen.getByLabelText("Command")).toHaveValue("bin/rails server -b 0.0.0.0 -p 3000");
    await user.click(screen.getByRole("radio", { name: "Production server" }));
    expect(screen.getByLabelText("Command")).toHaveValue("bundle exec puma -b tcp://0.0.0.0:3000");
    await user.selectOptions(screen.getByLabelText("Framework preset"), "rack");
    expect(screen.getByLabelText("Port inside the container")).toHaveValue(9292);
    await user.click(screen.getByLabelText(/Debug with rdbg/));
    expect(screen.getByLabelText("rdbg port inside the container")).toHaveValue(12345);
    expect(screen.getByLabelText("Command")).toHaveValue("rdbg --open … -c -- bundle exec puma -b tcp://0.0.0.0:9292");
    expect(rubyServerRequest(last!)).toEqual({ server: true, mode: "production", preset: "rack", port: 9292, debug: true, debugPort: 12345 });
    expect(rubyCommandHint("rack", "dev", "", false)).toBe("bundle exec puma -b tcp://0.0.0.0:9292");
  });
});

describe("Ruby card", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("leads the Runtime tab, shows the server and rdbg ports and saves", async () => {
    const project = makeProject({
      serves: "ruby",
      appService: "ruby",
      hostnames: ["acme-shop.test"],
      services: [
        { kind: "ruby", variant: "ruby", version: "4.0", image: "ghcr.io/envoryx/envoryx-ruby:4.0", enabled: true, config: { server: true, mode: "dev", preset: "rails", port: 3000, hostPort: 20001, debug: true, debugPort: 12345, debugHostPort: 20002 } },
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
    expect(headings.indexOf("Ruby")).toBeLessThan(headings.indexOf("PHP"));
    const card = screen.getByRole("heading", { name: "Ruby" }).closest(".rounded-xl") as HTMLElement;
    expect(within(card).getByText("host port 20001")).toBeInTheDocument();
    expect(within(card).getByText("rdbg on host port 20002")).toBeInTheDocument();
    expect(within(card).getByLabelText("Framework preset")).toHaveValue("rails");
    await user.click(within(card).getByRole("radio", { name: "Production server" }));
    await user.click(within(card).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({
      ruby: { enabled: true, version: "4.0", server: true, mode: "production", preset: "rails", port: 3000, debug: true, debugPort: 12345 },
    });
  });
});
