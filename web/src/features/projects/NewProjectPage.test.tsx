import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { NewProjectPage } from "./NewProjectPage";
import { authedRoutes, makeProject, mockApi, renderApp, runtimesFixture } from "@/test/utils";
import type { Preview, RuntimesResponse } from "@/api/types";

/** The PHP fixture plus what a backend with Node-only support adds: Node templates and presets. */
const nodeRuntimesFixture: RuntimesResponse = {
  ...runtimesFixture,
  templates: [
    ...(runtimesFixture.templates ?? []).map((tpl) => ({ ...tpl, runtime: "php" as const })),
    { id: "vite", name: "Vite + React (TypeScript)", description: "Vite scaffold with React and TypeScript – dev server with HMR on the project URL.", docroot: "dist", requiresDatabase: false, runtime: "node", node: { devServer: true, preset: "vite", port: 5173, script: "dev" } },
    { id: "next", name: "Next.js (App Router, TypeScript)", description: "create-next-app with the App Router and TypeScript.", docroot: "", requiresDatabase: false, runtime: "node", node: { devServer: true, preset: "next", port: 3000, script: "dev" } },
  ],
  nodePresets: [
    { key: "vite", label: "Vite (Vue, React, Svelte, Laravel…)", port: 5173 },
    { key: "next", label: "Next.js", port: 3000 },
    { key: "nuxt", label: "Nuxt", port: 3000 },
    { key: "generic", label: "Other (HOST/PORT env only)", port: 5173 },
  ],
};

/** The Node fixture plus Python templates and presets. */
const pythonRuntimesFixture: RuntimesResponse = {
  ...nodeRuntimesFixture,
  templates: [
    ...(nodeRuntimesFixture.templates ?? []),
    { id: "django", name: "Django", description: "django-admin startproject", docroot: "", requiresDatabase: false, recommendedDatabase: "postgresql", runtime: "python", python: { server: true, preset: "django", port: 8000, app: "config.wsgi:application" } },
    { id: "fastapi", name: "FastAPI", description: "A minimal FastAPI application", docroot: "", requiresDatabase: false, runtime: "python", python: { server: true, preset: "asgi", port: 8000, app: "main:app" } },
  ],
  pythonPresets: [
    { key: "django", label: "Django", port: 8000, app: "config.wsgi:application", appLabel: "WSGI application (production mode)", appHint: "e.g. config.wsgi:application" },
    { key: "flask", label: "Flask", port: 5000, app: "app:app", appLabel: "Application", appHint: "module:attribute" },
    { key: "asgi", label: "FastAPI / ASGI (uvicorn)", port: 8000, app: "main:app", appLabel: "ASGI application", appHint: "module:attribute" },
  ],
};

const emptyPreview: Preview = { slug: "acme-shop", path: "/projects/acme-shop", hostPath: "/x", httpPort: 20000, network: "n", containers: [], volumes: [], images: [], warnings: [] };

/** Records the preview body and answers with a plan derived from the request, like the backend does. */
function previewRoute(onBody: (b: Record<string, unknown>) => void, extra: Partial<Preview> = {}) {
  return (_u: string, init: RequestInit) => {
    const body = JSON.parse(init.body as string) as Record<string, unknown>;
    onBody(body);
    const serves = body.php ? "php" : (body.python as { server?: boolean } | undefined)?.server ? "python" : (body.node as { devServer?: boolean } | undefined)?.devServer ? "node" : "static";
    return { body: { preview: { ...emptyPreview, serves, ...extra } } };
  };
}

describe("NewProjectPage wizard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("walks through the steps, previews and creates", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": () => ({
        body: {
          preview: {
            slug: "acme-shop",
            path: "/projects/acme-shop",
            hostPath: "/mnt/user/development/acme-shop",
            httpPort: 20000,
            network: "envoryx-acme-shop",
            containers: [
              { service: "php", name: "envoryx-acme-shop-php", image: "ghcr.io/envoryx/envoryx-php:8.4", ports: [], mounts: ["/mnt/user/development/acme-shop → /var/www/html"] },
              { service: "web", name: "envoryx-acme-shop-web", image: "caddy:2-alpine", ports: ["20000 → 80/tcp"], mounts: [] },
            ],
            volumes: [],
            images: ["ghcr.io/envoryx/envoryx-php:8.4", "caddy:2-alpine"],
            warnings: ["image ghcr.io/envoryx/envoryx-php:8.4 will be pulled on first start"],
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
    await user.type(screen.getByLabelText("Project name"), "Acme Shop");
    expect(screen.getByText("Identifier: acme-shop")).toBeInTheDocument();
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
    await user.selectOptions(screen.getByLabelText("Web server"), "apache");
    expect(screen.getByLabelText("Version")).toHaveValue("2.4");
    expect(screen.getByText(/honours \.htaccess/)).toBeInTheDocument();
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

    expect(await screen.findByText("envoryx-acme-shop-php")).toBeInTheDocument();
    expect(screen.getByText("envoryx-acme-shop-web")).toBeInTheDocument();
    expect(screen.getByText(/will be pulled/)).toBeInTheDocument();
    const preview = api.calls.find((c) => c.url.endsWith("/projects/preview"))?.body as Record<string, unknown>;
    expect(preview).toMatchObject({ name: "Acme Shop", path: "acme-shop", docroot: "public", php: { version: "8.3" }, node: { version: "24" }, database: { type: "mariadb", version: "11", exposePort: true }, mailpit: {}, env: [{ key: "APP_ENV", value: "local" }] });

    await user.click(screen.getByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    expect(create?.body).toMatchObject({ name: "Acme Shop", start: true, createStarter: true, web: { type: "apache", version: "2.4" } });
  });

  it("with a repository, asks the server to apply its envoryx.yml unless unticked", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": previewRoute(() => {}),
      "POST /projects": () => ({ status: 201, body: { project: makeProject(), manifest: { changes: [], missingSecrets: [], inSync: true } } }),
    });
    renderApp(
      <Routes>
        <Route path="/projects/new" element={<NewProjectPage />} />
        <Route path="/projects/:id" element={<h1>Detail</h1>} />
      </Routes>,
      { route: "/projects/new" },
    );
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Acme Shop");
    expect(screen.queryByLabelText(/Use the repository's envoryx\.yml/)).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("Repository URL"), "https://github.com/acme/shop.git");
    expect(screen.getByLabelText(/Use the repository's envoryx\.yml/)).toBeChecked();
    for (let i = 0; i < 5; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText(/that file replaces the services chosen here/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    expect(create?.body).toMatchObject({ git: { url: "https://github.com/acme/shop.git" }, useManifest: true, createStarter: false });
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
    await screen.findByText("Latest WordPress");
    expect(previewBody).toMatchObject({ template: "wordpress", docroot: "", database: { type: "mariadb" } });
  });

  it("creates a Node-only project: no php key, dev server on, no starter", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: nodeRuntimesFixture }),
      "POST /projects/preview": previewRoute((b) => (previewBody = b), {
        devHostname: "acme-shop-dev.test",
        containers: [
          { service: "node", name: "envoryx-acme-shop-node", image: "ghcr.io/envoryx/envoryx-node:24", ports: ["20001 → 5173/tcp"], mounts: [] },
          { service: "web", name: "envoryx-acme-shop-web", image: "caddy:2-alpine", ports: [], mounts: [] },
        ],
        warnings: ['the dev server runs "npm run dev" but nothing creates a package.json – pick a Node template, clone a repository or scaffold from the Node terminal; until then the container waits'],
      }),
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Acme Shop");
    expect(screen.getByRole("radio", { name: "PHP application" })).toBeChecked();
    // Templates follow the stack: PHP ones disappear, Node ones show up.
    expect(screen.getByRole("radio", { name: /WordPress/ })).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "Node.js application" }));
    expect(screen.queryByRole("radio", { name: /WordPress/ })).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Vite \+ React/ })).toBeInTheDocument();
    expect(screen.getByLabelText("Document root")).toHaveValue("");
    expect(screen.getByText(/Not used while the dev server serves the app/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Continue" }));

    // Runtimes step: the Node card comes first with the dev server preselected; PHP is off.
    expect(await screen.findByLabelText(/Enable Node\.js/)).toBeChecked();
    expect(screen.getByLabelText(/Enable PHP/)).not.toBeChecked();
    expect(screen.getByLabelText(/Run a dev server/)).toBeChecked();
    expect(screen.getByLabelText("Framework preset")).toHaveValue("vite");
    expect(screen.getByText(/answers on the project URL; <slug>-dev/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Continue" }));

    expect(await screen.findByText(/The web server is part of every project/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/SPA fallback/)).not.toBeInTheDocument();
    for (let i = 0; i < 3; i++) await user.click(screen.getByRole("button", { name: "Continue" }));

    expect(await screen.findByText("Node dev server (the HTTP port stays unpublished)")).toBeInTheDocument();
    // No proxy settings in this test: the URL row shows the direct port, which must be the
    // node container's published host port – the web HTTP port (20000) has nothing listening.
    expect(screen.getByText("http://localhost:20001")).toBeInTheDocument();
    expect(screen.queryByText(/:20000/)).not.toBeInTheDocument();
    expect(screen.getByText(/nothing creates a package.json/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/Create starter/)).not.toBeInTheDocument();
    expect(screen.queryByText("Dev server URL")).not.toBeInTheDocument();
    expect(previewBody).not.toHaveProperty("php");
    expect(previewBody).toMatchObject({ docroot: "", createStarter: false, node: { version: "24", devServer: true, preset: "vite", port: 5173, script: "dev" } });
  });

  it("creates a Python project: server on, no php key, no starter, direct URL on the python port", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: pythonRuntimesFixture }),
      "POST /projects/preview": previewRoute((b) => (previewBody = b), {
        containers: [
          { service: "python", name: "envoryx-acme-shop-python", image: "ghcr.io/envoryx/envoryx-python:3.13", ports: ["20001 → 8000/tcp"], mounts: [] },
          { service: "web", name: "envoryx-acme-shop-web", image: "caddy:2-alpine", ports: [], mounts: [] },
        ],
      }),
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Acme Shop");
    await user.click(screen.getByRole("radio", { name: "Python application" }));
    // Templates follow the stack.
    expect(screen.queryByRole("radio", { name: /WordPress/ })).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Django/ })).toBeInTheDocument();
    expect(screen.getByText(/Not used while the application server serves the app/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Continue" }));

    // Runtimes step: the Python card leads with the server on; PHP and Node are off.
    expect(await screen.findByLabelText(/Enable Python/)).toBeChecked();
    expect(screen.getByLabelText(/Enable PHP/)).not.toBeChecked();
    expect(screen.getByLabelText(/Enable Node\.js/)).not.toBeChecked();
    expect(screen.getByLabelText(/Run the application server/)).toBeChecked();
    expect(screen.getByLabelText("Framework preset")).toHaveValue("django");
    expect(screen.getByLabelText("Command")).toHaveValue("python manage.py runserver 0.0.0.0:8000");
    // Switching the preset follows its port and app while untouched; the command reflects it.
    await user.selectOptions(screen.getByLabelText("Framework preset"), "asgi");
    expect(screen.getByLabelText("ASGI application")).toHaveValue("main:app");
    expect(screen.getByLabelText("Command")).toHaveValue("uvicorn main:app --host 0.0.0.0 --port 8000 --reload");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    expect(await screen.findByText(/The application server answers on the project URL\. The web server is part of every project/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/SPA fallback/)).not.toBeInTheDocument();
    for (let i = 0; i < 3; i++) await user.click(screen.getByRole("button", { name: "Continue" }));

    expect(await screen.findByText("Python application server (the HTTP port stays unpublished)")).toBeInTheDocument();
    expect(screen.getByText("http://localhost:20001")).toBeInTheDocument();
    expect(screen.queryByLabelText(/Create starter/)).not.toBeInTheDocument();
    expect(previewBody).not.toHaveProperty("php");
    expect(previewBody).not.toHaveProperty("node");
    expect(previewBody).toMatchObject({ docroot: "", createStarter: false, python: { version: "3.13", server: true, mode: "dev", preset: "asgi", app: "main:app", port: 8000, debug: false } });
  });

  it("a Python template presets the server from its defaults", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: pythonRuntimesFixture }),
      "POST /projects/preview": previewRoute((b) => (previewBody = b)),
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Acme Shop");
    await user.click(screen.getByRole("radio", { name: "Python application" }));
    await user.click(screen.getByRole("radio", { name: /FastAPI/ }));
    expect(screen.getByLabelText("Repository URL")).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByLabelText("Framework preset")).toHaveValue("asgi");
    for (let i = 0; i < 4; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText(/A minimal FastAPI application/);
    expect(previewBody).toMatchObject({ template: "fastapi", python: { server: true, preset: "asgi", port: 8000, app: "main:app" } });
  });

  it("a Node template presets the dev server and docroot from its defaults", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: nodeRuntimesFixture }),
      "POST /projects/preview": previewRoute((b) => (previewBody = b)),
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Acme Shop");
    await user.click(screen.getByRole("radio", { name: "Node.js application" }));
    await user.click(screen.getByRole("radio", { name: /Next\.js/ }));
    expect(screen.getByLabelText("Repository URL")).toBeDisabled();
    await user.click(screen.getByRole("radio", { name: /Vite \+ React/ }));
    expect(screen.getByLabelText("Document root")).toHaveValue("dist");
    for (let i = 0; i < 5; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText(/Vite scaffold with React/);
    expect(previewBody).toMatchObject({ template: "vite", docroot: "dist", node: { devServer: true, preset: "vite", port: 5173, script: "dev" } });
  });

  it("switching the stack drops a template of the other runtime", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: nodeRuntimesFixture }),
      "POST /projects/preview": (_u, init) => {
        const body = JSON.parse(init.body as string) as Record<string, unknown>;
        previewBody = body;
        if (body.template === "wordpress" && !body.php) return { status: 422, body: { error: { code: "validation_failed", message: "invalid input: template wordpress needs PHP" } } };
        return { body: { preview: { ...emptyPreview, serves: "node" } } };
      },
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Blog");
    await user.click(screen.getByRole("radio", { name: /WordPress/ }));
    expect(screen.getByLabelText("Document root")).toHaveValue("");
    await user.click(screen.getByRole("radio", { name: "Node.js application" }));
    expect(screen.getByRole("radio", { name: /^Blank/ })).toBeChecked();
    expect(screen.getByLabelText("Repository URL")).toBeEnabled();
    for (let i = 0; i < 5; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText("Node dev server (the HTTP port stays unpublished)");
    expect(screen.queryByText(/needs PHP/)).not.toBeInTheDocument();
    expect(previewBody).not.toHaveProperty("template");
    expect(previewBody).not.toHaveProperty("php");
  });

  it("static site: starter index.html and the SPA fallback", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: nodeRuntimesFixture }),
      "POST /projects/preview": previewRoute((b) => (previewBody = b)),
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Docs");
    await user.click(screen.getByRole("radio", { name: "Static site" }));
    // Only Blank is left to start from.
    expect(screen.getAllByRole("radio", { name: /Blank|WordPress|Laravel|Vite|Next/ })).toHaveLength(1);
    expect(screen.getByText(/Build output served by the web server/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByLabelText(/Enable PHP/)).not.toBeChecked();
    expect(screen.getByLabelText(/Enable Node\.js/)).not.toBeChecked();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText("The web server serves static files from the document root.")).toBeInTheDocument();
    expect(screen.getByText(/Caddy serves the document root statically/)).toBeInTheDocument();
    await user.click(screen.getByLabelText(/SPA fallback to index.html/));
    for (let i = 0; i < 3; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByLabelText(/Create starter index\.html/)).toBeChecked();
    expect(previewBody).not.toHaveProperty("php");
    expect(previewBody).not.toHaveProperty("node");
    expect(previewBody).toMatchObject({ createStarter: true, docroot: "", web: { type: "caddy", version: "2", spaFallback: true } });
  });

  it("leaving the Node stack turns the dev server default back off", async () => {
    mockApi({ ...authedRoutes, "GET /runtimes": () => ({ body: nodeRuntimesFixture }) });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "App");
    await user.click(screen.getByRole("radio", { name: "Node.js application" }));
    await user.click(screen.getByRole("radio", { name: "PHP application" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    // Node as a toolchain next to PHP starts without a dev server, like a fresh PHP flow.
    await user.click(await screen.findByLabelText(/Enable Node\.js/));
    expect(screen.getByLabelText(/Run a dev server/)).not.toBeChecked();
  });

  it("turning the dev server off on the Node stack suggests dist as the document root", async () => {
    mockApi({ ...authedRoutes, "GET /runtimes": () => ({ body: nodeRuntimesFixture }) });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "App");
    await user.click(screen.getByRole("radio", { name: "Node.js application" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByLabelText(/Run a dev server/));
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(await screen.findByLabelText("Document root")).toHaveValue("dist");
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

  it("imports an existing website: uploads, fills the next steps and creates with the upload", async () => {
    const analysis = {
      format: "zip",
      root: "public_html/",
      files: 1234,
      bytes: 52_428_800,
      framework: { id: "wordpress", name: "WordPress", version: "6.2.2" },
      runtime: "php",
      phpVersion: "8.3",
      phpExtensions: ["mysqli"],
      docroot: "",
      web: "apache",
      database: "mariadb",
      config: { path: "wp-config.php", mode: "adapt" },
      dump: { bytes: 1000, compressed: false, variant: "mariadb", tool: "mariadb-dump", server: "10.11.6-MariaDB" },
      notices: [{ level: "info", text: "The archive's folder {{folder}} became the project directory.", params: { folder: "public_html" } }],
    };
    const sent: { fields: string[] } = { fields: [] };
    // Only XMLHttpRequest reports upload progress, so the upload does not go through fetch.
    class FakeXHR {
      status = 0;
      responseText = "";
      upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = { onprogress: null };
      onload: (() => void) | null = null;
      onerror: (() => void) | null = null;
      withCredentials = false;
      open(method: string, url: string) {
        expect(`${method} ${url}`).toBe("POST /api/v1/site-imports");
      }
      setRequestHeader() {}
      send(form: FormData) {
        sent.fields = [...form.keys()];
        this.upload.onprogress?.({ lengthComputable: true, loaded: 5, total: 10 });
        this.status = 201;
        this.responseText = JSON.stringify({ import: { id: "11111111-2222-4333-8444-555555555555", siteName: "old_blog.zip", dumpName: "dump.sql", createdAt: "", expiresAt: "", analysis } });
        setTimeout(() => this.onload?.(), 0);
      }
    }
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    let previewBody: Record<string, unknown> | undefined;
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": previewRoute((b) => (previewBody = b)),
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
    await screen.findByLabelText("Project name");
    await user.click(screen.getByRole("radio", { name: /Existing website/ }));
    expect(screen.queryByLabelText("Repository URL")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upload and analyse" })).toBeDisabled();
    await user.upload(screen.getByLabelText("Website archive"), new File(["PK"], "old_blog.zip", { type: "application/zip" }));
    await user.upload(screen.getByLabelText("Database dump (optional)"), new File(["-- dump"], "dump.sql"));
    await user.click(screen.getByRole("button", { name: "Upload and analyse" }));

    expect(await screen.findByText("Recognised: WordPress 6.2.2")).toBeInTheDocument();
    expect(sent.fields).toEqual(["site", "database"]);
    expect(screen.getByText("The archive's folder public_html became the project directory.")).toBeInTheDocument();
    expect(screen.getByLabelText(/Adapt the configuration to Envoryx/)).toBeChecked();
    // The name comes from the archive when none was typed.
    expect(screen.getByLabelText("Project name")).toHaveValue("old blog");

    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByLabelText("PHP version")).toHaveValue("8.3");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByLabelText("Web server")).toHaveValue("apache");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByRole("radio", { name: "MariaDB" })).toBeChecked();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByRole("button", { name: "Continue" }));
    expect(await screen.findByText(/the dump is imported into the project database/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/Create starter/)).not.toBeInTheDocument();
    await waitFor(() => expect(previewBody).toBeDefined());
    await user.click(screen.getByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    expect(create?.body).toMatchObject({
      name: "old blog",
      import: { id: "11111111-2222-4333-8444-555555555555", adaptConfig: true },
      createStarter: false,
      docroot: "",
      web: { type: "apache" },
      database: { type: "mariadb" },
      php: { version: "8.3" },
    });
    const body = create?.body as { php: { config: { extensions: string[] } }; template?: string; git?: unknown };
    expect(body.php.config.extensions).toContain("mysqli");
    expect(body.template).toBeUndefined();
    expect(body.git).toBeUndefined();
  });

  it("adds further databases with a name of their own", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": previewRoute(() => {}),
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
    await user.type(await screen.findByLabelText("Project name"), "Acme Shop");
    for (let i = 0; i < 3; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByRole("radio", { name: "MariaDB" }));
    await user.click(screen.getByRole("button", { name: "Add a database" }));
    await user.type(screen.getByLabelText("Name"), "Bad Name");
    expect(screen.getByText("Lowercase letters, digits and dashes, starting with a letter.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
    await user.clear(screen.getByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "analytics");
    for (let i = 0; i < 2; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    expect(create?.body).toMatchObject({ database: { type: "mariadb" }, databases: [{ name: "analytics", type: "mariadb", version: "11" }] });
  });

  it("adds Ollama with the GPU", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": previewRoute(() => {}),
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
    await user.type(await screen.findByLabelText("Project name"), "Acme Chat");
    for (let i = 0; i < 3; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByRole("checkbox", { name: /^Ollama/ }));
    await user.click(screen.getByRole("checkbox", { name: /^Use the GPU/ }));
    for (let i = 0; i < 2; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    expect(create?.body).toMatchObject({ ollama: { exposePort: false, gpu: true } });
  });
});
