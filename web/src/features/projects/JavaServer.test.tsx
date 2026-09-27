import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Route, Routes } from "react-router-dom";
import { ProjectDetailPage } from "./ProjectDetailPage";
import { JavaServerFields, defaultJavaServerForm, javaCommandHint, javaServerRequest, type JavaServerForm } from "./JavaServerFields";
import { authedRoutes, makeProject, mockApi, runtimesFixture, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const proxy = { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true };

function Harness({ onChange }: { onChange: (v: JavaServerForm) => void }) {
  const [value, setValue] = useState<JavaServerForm>({ ...defaultJavaServerForm, server: true });
  return (
    <JavaServerFields
      value={value}
      onChange={(v) => {
        setValue(v);
        onChange(v);
      }}
    />
  );
}

describe("JavaServerFields", () => {
  it("shows the command per preset, asks for a jar only for the jar preset and builds the request", async () => {
    let last: JavaServerForm | undefined;
    render(<Harness onChange={(v) => (last = v)} />);
    const user = userEvent.setup();
    expect(screen.getByLabelText("Command")).toHaveValue("mvn spring-boot:run | gradle bootRun");
    expect(screen.queryByLabelText("Jar")).not.toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Framework preset"), "quarkus");
    expect(screen.getByLabelText("Command")).toHaveValue("mvn quarkus:dev | gradle quarkusDev");
    await user.click(screen.getByRole("radio", { name: "Production server" }));
    expect(screen.getByLabelText("Command")).toHaveValue("mvn package | gradle build, then java -jar target/quarkus-app/quarkus-run.jar");

    // The jar preset has no dev mode of its own, so the mode choice goes away.
    await user.selectOptions(screen.getByLabelText("Framework preset"), "jar");
    expect(screen.queryByRole("radio", { name: "Production server" })).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("Jar"), "build/libs/app.jar");
    await user.click(screen.getByLabelText(/Debug with JDWP/));
    expect(screen.getByLabelText("JDWP port inside the container")).toHaveValue(5005);
    expect(javaServerRequest(last!)).toEqual({ server: true, mode: "production", preset: "jar", jar: "build/libs/app.jar", port: 8080, debug: true, debugPort: 5005 });
    // Leaving the jar preset drops the jar from the request.
    expect(javaServerRequest({ ...last!, preset: "spring-boot" }).jar).toBe("");
    expect(javaCommandHint("jar", "dev", "")).toBe("mvn package | gradle build, then java -jar target/*.jar | build/libs/*.jar");
  });
});

describe("Java card", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("leads the Runtime tab, shows the server and JDWP ports and saves", async () => {
    const project = makeProject({
      serves: "java",
      appService: "java",
      hostnames: ["acme-shop.test"],
      services: [
        { kind: "java", variant: "java", version: "25", image: "ghcr.io/envoryx/envoryx-java:25", enabled: true, config: { server: true, mode: "dev", preset: "spring-boot", port: 8080, hostPort: 20001, debug: true, debugPort: 5005, debugHostPort: 20002 } },
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
    expect(headings.indexOf("Java")).toBeLessThan(headings.indexOf("PHP"));
    const card = screen.getByRole("heading", { name: "Java" }).closest(".rounded-xl") as HTMLElement;
    expect(within(card).getByText("host port 20001")).toBeInTheDocument();
    expect(within(card).getByText("JDWP on host port 20002")).toBeInTheDocument();
    expect(within(card).getByLabelText("Framework preset")).toHaveValue("spring-boot");
    await user.click(within(card).getByRole("radio", { name: "Production server" }));
    await user.click(within(card).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({
      java: { enabled: true, version: "25", server: true, mode: "production", preset: "spring-boot", jar: "", port: 8080, debug: true, debugPort: 5005 },
    });
  });
});
