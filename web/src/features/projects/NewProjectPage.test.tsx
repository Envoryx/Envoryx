import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { NewProjectPage } from "./NewProjectPage";
import { authedRoutes, makeProject, mockApi, renderApp, runtimesFixture } from "@/test/utils";

describe("NewProjectPage wizard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("walks through the steps, previews and creates", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": () => ({
        body: {
          preview: {
            slug: "shimly-api",
            path: "/projects/shimly-api",
            hostPath: "/mnt/user/development/shimly-api",
            httpPort: 20000,
            network: "staqio-shimly-api",
            containers: [
              { service: "php", name: "staqio-shimly-api-php", image: "ghcr.io/seramos/staqio-php:8.4", ports: [], mounts: ["/mnt/user/development/shimly-api → /var/www/html"] },
              { service: "web", name: "staqio-shimly-api-web", image: "caddy:2-alpine", ports: ["20000 → 80/tcp"], mounts: [] },
            ],
            volumes: [],
            images: ["ghcr.io/seramos/staqio-php:8.4", "caddy:2-alpine"],
            warnings: ["image ghcr.io/seramos/staqio-php:8.4 will be pulled on first start"],
          },
        },
      }),
      "POST /projects": () => ({ status: 201, body: { project: makeProject() } }),
    });
    renderApp(
      <Routes>
        <Route path="/projects/new" element={<NewProjectPage />} />
        <Route path="/projects/:id" element={<h1>Detail</h1>} />
      </Routes>,
      { route: "/projects/new" },
    );
    const user = userEvent.setup();

    const cont = async () => user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByLabelText("Project name")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
    await user.type(screen.getByLabelText("Project name"), "Shimly API");
    expect(screen.getByText("Identifier: shimly-api")).toBeInTheDocument();
    await cont();

    // Runtime step: versions come from the API, not the UI.
    const version = await screen.findByLabelText("PHP version");
    expect(version).toHaveValue("8.4");
    await user.selectOptions(version, "8.3");
    expect(screen.getByLabelText(/gd/)).toBeDisabled();
    await user.click(screen.getByLabelText(/Enable Node\.js/));
    expect(screen.getByLabelText("Node.js version")).toHaveValue("24");
    await cont();

    expect(await screen.findByLabelText("Web server")).toHaveValue("caddy");
    await cont();
    expect(await screen.findByText("Database")).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "MariaDB" }));
    expect(screen.getByLabelText("Version")).toHaveValue("11");
    await user.click(screen.getByLabelText(/Publish database port/));
    await user.click(screen.getByLabelText(/^Mailpit/));
    await cont();
    await user.click(await screen.findByRole("button", { name: "Add variable" }));
    await user.type(screen.getByLabelText("Variable name"), "app_env");
    expect(screen.getByLabelText("Variable name")).toHaveValue("APP_ENV");
    await user.type(screen.getByLabelText("Variable value"), "local");
    await cont();

    expect(await screen.findByText("staqio-shimly-api-php")).toBeInTheDocument();
    expect(screen.getByText("staqio-shimly-api-web")).toBeInTheDocument();
    expect(screen.getByText(/will be pulled/)).toBeInTheDocument();
    const preview = api.calls.find((c) => c.url.endsWith("/projects/preview"))?.body as Record<string, unknown>;
    expect(preview).toMatchObject({ name: "Shimly API", path: "shimly-api", docroot: "public", php: { version: "8.3" }, node: { version: "24" }, database: { type: "mariadb", version: "11", exposePort: true }, mailpit: {}, env: [{ key: "APP_ENV", value: "local" }] });

    await user.click(screen.getByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    expect(create?.body).toMatchObject({ name: "Shimly API", start: true, createStarter: true });
  });

  it("selecting a template presets docroot and database and disables git", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": (_u, init) => {
        previewBody = JSON.parse(init.body as string);
        return { body: { preview: { slug: "blog", path: "/projects/blog", hostPath: "/x", httpPort: 20000, network: "n", containers: [], volumes: [], images: [], warnings: [] } } };
      },
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Blog");
    await user.click(screen.getByRole("radio", { name: /WordPress/ }));
    expect(screen.getByLabelText("Document root")).toHaveValue("");
    expect(screen.getByLabelText("Repository URL")).toBeDisabled();
    for (let i = 0; i < 5; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText(/Scaffolding runs while the project is created/);
    expect(previewBody).toMatchObject({ template: "wordpress", docroot: "", database: { type: "mariadb" } });
  });

  it("shows validation errors from the preview", async () => {
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": () => ({ status: 422, body: { error: { code: "validation_failed", message: "invalid input: path must stay inside the projects directory" } } }),
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Evil");
    await user.type(screen.getByLabelText("Project directory"), "../etc");
    for (let i = 0; i < 5; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText(/must stay inside/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create project" })).toBeDisabled();
  });
});
