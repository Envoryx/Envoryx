import { screen, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { NewProjectPage } from "./NewProjectPage";
import { authedRoutes, lockedMongoRuntimesFixture, makeProject, mockApi, renderApp, runtimesFixture } from "@/test/utils";
import type { Preview, RuntimesResponse } from "@/api/types";

/** The PHP fixture plus what a backend with Node-only support adds: Node templates and presets. */
const nodeRuntimesFixture: RuntimesResponse = {
  ...runtimesFixture,
  templates: [
    ...(runtimesFixture.templates ?? []).map((tpl) => ({ ...tpl, runtime: "php" as const })),
    { id: "vite", name: "Vite + React (TypeScript)", description: "Vite scaffold with React and TypeScript - dev server with HMR on the project URL.", docroot: "dist", requiresDatabase: false, runtime: "node", node: { devServer: true, preset: "vite", port: 5173, script: "dev" } },
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

/** Renders the wizard with a detail route to land on after creating. */
function renderWizard() {
  renderApp(
    <Routes>
      <Route path="/projects/new" element={<NewProjectPage />} />
      <Route path="/projects/:id" element={<h1>Detail</h1>} />
    </Routes>,
    { route: "/projects/new" },
  );
}

const cont = (user: UserEvent) => user.click(screen.getByRole("button", { name: "Continue" }));

/** Step 1: an empty project, optionally with another runtime than the preselected PHP. */
async function startEmpty(user: UserEvent, stack?: string) {
  await user.click(await screen.findByRole("radio", { name: /^Empty project/ }));
  if (stack) await user.click(screen.getByRole("radio", { name: stack }));
}

/** Step 1: a template from the gallery; one outside the popular short list needs "Show all" first. */
async function pickTemplate(user: UserEvent, name: RegExp) {
  await screen.findByRole("button", { name: "Continue" });
  if (!screen.queryByRole("button", { name })) await user.click(screen.getByRole("button", { name: /^Show all \d+ templates/ }));
  await user.click(screen.getByRole("button", { name }));
}

/** Step 2: the project name. */
async function nameIt(user: UserEvent, name: string) {
  await user.type(await screen.findByLabelText("Project name"), name);
}

const openAdvanced = (user: UserEvent) => user.click(screen.getByRole("button", { name: /^Advanced settings/ }));
const openTools = (user: UserEvent) => user.click(screen.getByRole("button", { name: "More runtimes as tools" }));

/** Continues from step 2 to the Create step. */
const toCreate = async (user: UserEvent) => cont(user);

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
    renderWizard();
    const user = userEvent.setup();

    // Nothing chosen yet: no way on.
    expect(await screen.findByRole("button", { name: "Continue" })).toBeDisabled();
    await startEmpty(user);
    await cont(user);

    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
    await nameIt(user, "Acme Shop");
    expect(screen.getByText("Reachable at acme-shop.test")).toBeInTheDocument();

    // Runtime: versions come from the API, not the UI.
    await openAdvanced(user);
    const version = screen.getByLabelText("PHP version");
    expect(version).toHaveValue("8.4");
    await user.selectOptions(version, "8.3");
    expect(screen.getByLabelText(/gd/)).toBeDisabled();
    await openTools(user);
    await user.click(screen.getByLabelText(/Enable Node\.js/));
    expect(screen.getByLabelText("Node.js version")).toHaveValue("24");

    expect(screen.getByLabelText("Web server")).toHaveValue("caddy");
    await user.selectOptions(screen.getByLabelText("Web server"), "apache");
    expect(screen.getByLabelText("Version", { selector: "#web-version" })).toHaveValue("2.4");
    expect(screen.getByText(/honours \.htaccess/)).toBeInTheDocument();

    expect(screen.getByRole("radiogroup", { name: "Database" })).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "MariaDB" }));
    expect(screen.getByLabelText("Version", { selector: "#db-version" })).toHaveValue("11");
    await user.click(screen.getByLabelText(/Publish database port/));
    await user.click(screen.getByLabelText(/^Mailpit/));

    await user.click(screen.getByRole("button", { name: "Add variable" }));
    await user.type(screen.getByLabelText("Variable name"), "app_env");
    expect(screen.getByLabelText("Variable name")).toHaveValue("APP_ENV");
    await user.type(screen.getByLabelText("Variable value"), "local");
    await toCreate(user);

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
    renderWizard();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("radio", { name: /^Git repository/ }));
    expect(screen.queryByLabelText(/Use the repository's envoryx\.yml/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
    await user.type(screen.getByLabelText("Repository URL"), "https://github.com/acme/shop.git");
    expect(screen.getByLabelText(/Use the repository's envoryx\.yml/)).toBeChecked();
    await cont(user);
    // Without a name, the repository names the project.
    expect(await screen.findByLabelText("Project name")).toHaveValue("shop");
    await toCreate(user);
    expect(await screen.findByText(/that file replaces the services chosen here/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    expect(create?.body).toMatchObject({ git: { url: "https://github.com/acme/shop.git" }, useManifest: true, createStarter: false });
  });

  it("selecting a template presets docroot and database; switching to a repository drops it", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": (_u, init) => {
        previewBody = JSON.parse(init.body as string);
        return { body: { preview: { slug: "blog", path: "/projects/blog", hostPath: "/x", httpPort: 20000, network: "n", containers: [], volumes: [], images: [], warnings: [] } } };
      },
    });
    renderWizard();
    const user = userEvent.setup();
    await pickTemplate(user, /^WordPress/);
    expect(screen.getByRole("button", { name: /^WordPress/ })).toHaveAttribute("aria-pressed", "true");
    await cont(user);
    await nameIt(user, "Blog");
    expect(screen.getByRole("radio", { name: "MariaDB" })).toBeChecked();
    await openAdvanced(user);
    expect(screen.getByLabelText("Document root")).toHaveValue("");
    await toCreate(user);
    await screen.findByText("Latest WordPress");
    expect(previewBody).toMatchObject({ template: "wordpress", docroot: "", database: { type: "mariadb" } });

    // Back to the start: a repository replaces the template.
    await user.click(screen.getByRole("button", { name: "Back" }));
    await user.click(screen.getByRole("button", { name: "Back" }));
    await user.click(await screen.findByRole("radio", { name: /^Git repository/ }));
    expect(screen.getByRole("button", { name: /^WordPress/ })).toHaveAttribute("aria-pressed", "false");
    await user.type(screen.getByLabelText("Repository URL"), "https://github.com/acme/blog.git");
    await cont(user);
    await toCreate(user);
    await waitFor(() => expect(previewBody).toHaveProperty("git"));
    expect(previewBody).not.toHaveProperty("template");
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
        warnings: ['the dev server runs "npm run dev" but nothing creates a package.json - pick a Node template, clone a repository or scaffold from the Node terminal; until then the container waits'],
      }),
    });
    renderWizard();
    const user = userEvent.setup();
    // The gallery shows every runtime; the filter narrows it to Node.js.
    expect(await screen.findByRole("button", { name: /^WordPress/ })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Node.js" }));
    expect(screen.queryByRole("button", { name: /^WordPress/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Vite \+ React/ })).toBeInTheDocument();
    await startEmpty(user);
    expect(screen.getByRole("radio", { name: "PHP application" })).toBeChecked();
    await user.click(screen.getByRole("radio", { name: "Node.js application" }));
    await cont(user);
    await nameIt(user, "Acme Shop");

    // The Node card leads with the dev server preselected; PHP waits among the tools, off.
    await openAdvanced(user);
    expect(screen.getByLabelText("Document root")).toHaveValue("");
    expect(screen.getByText(/Not used while the dev server serves the app/)).toBeInTheDocument();
    expect(screen.getByLabelText(/Enable Node\.js/)).toBeChecked();
    expect(screen.queryByLabelText(/Enable PHP/)).not.toBeInTheDocument();
    await openTools(user);
    expect(screen.getByLabelText(/Enable PHP/)).not.toBeChecked();
    expect(screen.getByLabelText(/Run a dev server/)).toBeChecked();
    expect(screen.getByLabelText("Framework preset")).toHaveValue("vite");
    expect(screen.getByText(/answers on the project URL; <slug>-dev/)).toBeInTheDocument();
    expect(screen.getByText(/The web server is part of every project/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/SPA fallback/)).not.toBeInTheDocument();
    await toCreate(user);

    expect(await screen.findByText("Node dev server (the HTTP port stays unpublished)")).toBeInTheDocument();
    // No proxy settings in this test: the URL row shows the direct port, which must be the
    // node container's published host port - the web HTTP port (20000) has nothing listening.
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
    renderWizard();
    const user = userEvent.setup();
    // The Python filter shows its templates only.
    await user.click(await screen.findByRole("button", { name: "Python" }));
    expect(screen.queryByRole("button", { name: /^WordPress/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Django/ })).toBeInTheDocument();
    await startEmpty(user, "Python application");
    await cont(user);
    await nameIt(user, "Acme Shop");

    // The Python card leads with the server on; PHP and Node wait among the tools, off.
    await openAdvanced(user);
    expect(screen.getByText(/Not used while the application server serves the app/)).toBeInTheDocument();
    expect(screen.getByLabelText(/Enable Python/)).toBeChecked();
    await openTools(user);
    expect(screen.getByLabelText(/Enable PHP/)).not.toBeChecked();
    expect(screen.getByLabelText(/Enable Node\.js/)).not.toBeChecked();
    expect(screen.getByLabelText(/Run the application server/)).toBeChecked();
    expect(screen.getByLabelText("Framework preset")).toHaveValue("django");
    expect(screen.getByLabelText("Command")).toHaveValue("python manage.py runserver 0.0.0.0:8000");
    // Switching the preset follows its port and app while untouched; the command reflects it.
    await user.selectOptions(screen.getByLabelText("Framework preset"), "asgi");
    expect(screen.getByLabelText("ASGI application")).toHaveValue("main:app");
    expect(screen.getByLabelText("Command")).toHaveValue("uvicorn main:app --host 0.0.0.0 --port 8000 --reload");
    expect(screen.getByText(/The application server answers on the project URL\. The web server is part of every project/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/SPA fallback/)).not.toBeInTheDocument();
    await toCreate(user);

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
    renderWizard();
    const user = userEvent.setup();
    await pickTemplate(user, /^FastAPI/);
    await cont(user);
    await nameIt(user, "Acme Shop");
    await openAdvanced(user);
    expect(screen.getByLabelText("Framework preset")).toHaveValue("asgi");
    await toCreate(user);
    await screen.findByText(/A minimal FastAPI application/);
    expect(previewBody).toMatchObject({ template: "fastapi", python: { server: true, preset: "asgi", port: 8000, app: "main:app" } });
  });

  it("offers only the runtimes and templates of the hoster's plan", async () => {
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: nodeRuntimesFixture }),
      "GET /plan": () => ({ body: { plan: { name: "Starter", limits: {}, runtimes: ["php"], disabled: [], lockedSettings: [] }, usage: { projects: 0, users: 1, diskBytes: 0 } } }),
    });
    renderWizard();
    const user = userEvent.setup();
    await screen.findByRole("button", { name: "Continue" });
    await waitFor(() => expect(screen.queryByRole("button", { name: /^Vite \+ React/ })).not.toBeInTheDocument());
    expect(screen.queryByRole("button", { name: /^Show all \d+ templates/ })).not.toBeInTheDocument();
    await startEmpty(user);
    expect(screen.getByRole("radio", { name: "PHP application" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Static site" })).toBeInTheDocument();
    expect(screen.queryByRole("radio", { name: "Node.js application" })).not.toBeInTheDocument();
  });

  it("a Node template presets the dev server and docroot from its defaults", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: nodeRuntimesFixture }),
      "POST /projects/preview": previewRoute((b) => (previewBody = b)),
    });
    renderWizard();
    const user = userEvent.setup();
    await pickTemplate(user, /^Next\.js/);
    await pickTemplate(user, /^Vite \+ React/);
    await cont(user);
    await nameIt(user, "Acme Shop");
    await openAdvanced(user);
    expect(screen.getByLabelText("Document root")).toHaveValue("dist");
    await toCreate(user);
    await screen.findByText(/Vite scaffold with React/);
    expect(previewBody).toMatchObject({ template: "vite", docroot: "dist", node: { devServer: true, preset: "vite", port: 5173, script: "dev" } });
  });

  it("an empty project on another runtime drops a template of the previous one", async () => {
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
    renderWizard();
    const user = userEvent.setup();
    await pickTemplate(user, /^WordPress/);
    await startEmpty(user, "Node.js application");
    expect(screen.getByRole("button", { name: /^WordPress/ })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("radio", { name: /^Empty project/ })).toHaveAttribute("aria-checked", "true");
    await cont(user);
    await nameIt(user, "Blog");
    await toCreate(user);
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
    renderWizard();
    const user = userEvent.setup();
    await startEmpty(user, "Static site");
    await cont(user);
    await nameIt(user, "Docs");
    await openAdvanced(user);
    expect(screen.getByText(/Build output served by the web server/)).toBeInTheDocument();
    // A static site has no runtime of its own: all of them wait among the tools, off.
    await openTools(user);
    expect(screen.getByLabelText(/Enable PHP/)).not.toBeChecked();
    expect(screen.getByLabelText(/Enable Node\.js/)).not.toBeChecked();
    expect(screen.getByText("The web server serves static files from the document root.")).toBeInTheDocument();
    expect(screen.getByText(/Caddy serves the document root statically/)).toBeInTheDocument();
    await user.click(screen.getByLabelText(/SPA fallback to index.html/));
    await toCreate(user);
    expect(await screen.findByLabelText(/Create starter index\.html/)).toBeChecked();
    expect(previewBody).not.toHaveProperty("php");
    expect(previewBody).not.toHaveProperty("node");
    expect(previewBody).toMatchObject({ createStarter: true, docroot: "", web: { type: "caddy", version: "2", spaFallback: true } });
  });

  it("leaving the Node stack turns the dev server default back off", async () => {
    mockApi({ ...authedRoutes, "GET /runtimes": () => ({ body: nodeRuntimesFixture }) });
    renderWizard();
    const user = userEvent.setup();
    await startEmpty(user, "Node.js application");
    await user.click(screen.getByRole("radio", { name: "PHP application" }));
    await cont(user);
    await nameIt(user, "App");
    await openAdvanced(user);
    await openTools(user);
    // Node as a toolchain next to PHP starts without a dev server, like a fresh PHP flow.
    await user.click(screen.getByLabelText(/Enable Node\.js/));
    expect(screen.getByLabelText(/Run a dev server/)).not.toBeChecked();
  });

  it("turning the dev server off on the Node stack suggests dist as the document root", async () => {
    mockApi({ ...authedRoutes, "GET /runtimes": () => ({ body: nodeRuntimesFixture }) });
    renderWizard();
    const user = userEvent.setup();
    await startEmpty(user, "Node.js application");
    await cont(user);
    await nameIt(user, "App");
    await openAdvanced(user);
    expect(screen.getByLabelText("Document root")).toHaveValue("");
    await user.click(screen.getByLabelText(/Run a dev server/));
    expect(screen.getByLabelText("Document root")).toHaveValue("dist");
  });

  it("shows validation errors from the preview", async () => {
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": () => ({ status: 422, body: { error: { code: "validation_failed", message: "invalid input: path must stay inside the projects directory" } } }),
    });
    renderWizard();
    const user = userEvent.setup();
    await startEmpty(user);
    await cont(user);
    await nameIt(user, "Evil");
    await openAdvanced(user);
    await user.type(screen.getByLabelText("Project directory"), "../etc");
    await toCreate(user);
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
    renderWizard();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("radio", { name: /^Existing website/ }));
    expect(screen.queryByLabelText("Repository URL")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Upload and analyse" })).toBeDisabled();
    await user.upload(screen.getByLabelText("Website archive"), new File(["PK"], "old_blog.zip", { type: "application/zip" }));
    await user.upload(screen.getByLabelText("Database dump (optional)"), new File(["-- dump"], "dump.sql"));
    await user.click(screen.getByRole("button", { name: "Upload and analyse" }));

    expect(await screen.findByText("Recognised: WordPress 6.2.2")).toBeInTheDocument();
    expect(sent.fields).toEqual(["site", "database"]);
    expect(screen.getByText("The archive's folder public_html became the project directory.")).toBeInTheDocument();
    expect(screen.getByLabelText(/Adapt the configuration to Envoryx/)).toBeChecked();
    await cont(user);

    // The name comes from the archive when none was typed.
    expect(await screen.findByLabelText("Project name")).toHaveValue("old blog");
    expect(screen.getByRole("radio", { name: "MariaDB" })).toBeChecked();
    await openAdvanced(user);
    expect(screen.getByLabelText("PHP version")).toHaveValue("8.3");
    expect(screen.getByLabelText("Web server")).toHaveValue("apache");
    await toCreate(user);
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
    renderWizard();
    const user = userEvent.setup();
    await startEmpty(user);
    await cont(user);
    await nameIt(user, "Acme Shop");
    await user.click(screen.getByRole("radio", { name: "MariaDB" }));
    await user.click(screen.getByRole("button", { name: "Add a database" }));
    await user.type(screen.getByLabelText("Name"), "Bad Name");
    expect(screen.getByText("Lowercase letters, digits and dashes, starting with a letter.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
    await user.clear(screen.getByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "analytics");
    await toCreate(user);
    await user.click(await screen.findByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    expect(create?.body).toMatchObject({ database: { type: "mariadb" }, databases: [{ name: "analytics", type: "mariadb", version: "11" }] });
  });

  it("locks the MongoDB versions the host kernel cannot start", async () => {
    mockApi({ ...authedRoutes, "GET /runtimes": () => ({ body: lockedMongoRuntimesFixture }) });
    renderWizard();
    const user = userEvent.setup();
    await startEmpty(user);
    await cont(user);
    await nameIt(user, "Acme Shop");
    await user.click(screen.getByRole("radio", { name: "MongoDB" }));
    const version = screen.getByLabelText("Version");
    expect(version).toHaveValue("8.2");
    const disabled = [...(version as HTMLSelectElement).options].filter((o) => o.disabled).map((o) => o.value);
    expect(disabled).toEqual(["8", "7"]);
    expect(screen.getByText(/switch to MongoDB 8\.2/)).toBeInTheDocument();
  });

  it("adds Ollama with the GPU", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": previewRoute(() => {}),
      "POST /projects": () => ({ status: 201, body: { project: makeProject() } }),
    });
    renderWizard();
    const user = userEvent.setup();
    await startEmpty(user);
    await cont(user);
    await nameIt(user, "Acme Chat");
    await user.click(screen.getByRole("checkbox", { name: /^Ollama/ }));
    await user.click(screen.getByRole("checkbox", { name: /^Use the GPU/ }));
    await toCreate(user);
    await user.click(await screen.findByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    expect(create?.body).toMatchObject({ ollama: { exposePort: false, gpu: true } });
  });

  it("connects the primary database to an external server", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": previewRoute(() => {}),
      "POST /projects": () => ({ status: 201, body: { project: makeProject() } }),
    });
    renderWizard();
    const user = userEvent.setup();
    await startEmpty(user);
    await cont(user);
    await nameIt(user, "Acme Ext");
    await user.click(screen.getByRole("radio", { name: "MariaDB" }));
    await user.click(screen.getByRole("radio", { name: "On an external server" }));
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
    await user.type(screen.getByLabelText("Host"), "host.docker.internal");
    await user.type(screen.getByLabelText("Database", { selector: "#db-ext-database" }), "shop");
    await user.type(screen.getByLabelText("Username"), "shop_app");
    await user.type(screen.getByLabelText("Password"), "pw");
    await toCreate(user);
    await user.click(await screen.findByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    expect(create?.body).toMatchObject({ database: { type: "mariadb", exposePort: false, external: { host: "host.docker.internal", port: 0, username: "shop_app", password: "pw", database: "shop" } } });
  });

  it("creates a Go project: server on, no php key, no starter", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": previewRoute(() => {}),
      "POST /projects": () => ({ status: 201, body: { project: makeProject() } }),
    });
    renderWizard();
    const user = userEvent.setup();
    await startEmpty(user, "Go application");
    await cont(user);
    await nameIt(user, "Acme Api");
    await openAdvanced(user);
    await user.clear(screen.getByLabelText("Main package"));
    await user.type(screen.getByLabelText("Main package"), "./cmd/api");
    await toCreate(user);
    expect(screen.queryByLabelText(/Create starter/)).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    const body = create?.body as Record<string, unknown>;
    expect(body.php).toBeUndefined();
    expect(body.createStarter).toBe(false);
    expect(body.go).toMatchObject({ version: "1.27", server: true, mode: "dev", package: "./cmd/api", port: 8080 });
  });

  it("creates a Ruby project: server on, no php key, no starter", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": previewRoute(() => {}),
      "POST /projects": () => ({ status: 201, body: { project: makeProject() } }),
    });
    renderWizard();
    const user = userEvent.setup();
    await startEmpty(user, "Ruby application");
    await cont(user);
    await nameIt(user, "Acme Blog");
    await openAdvanced(user);
    await user.selectOptions(screen.getByLabelText("Framework preset"), "rack");
    await toCreate(user);
    expect(screen.queryByLabelText(/Create starter/)).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    const body = create?.body as Record<string, unknown>;
    expect(body.php).toBeUndefined();
    expect(body.go).toBeUndefined();
    expect(body.createStarter).toBe(false);
    expect(body.ruby).toMatchObject({ version: "4.0", server: true, mode: "dev", preset: "rack", port: 9292 });
  });

  it("creates a Java project: server on, no php key, no starter, the jar only for the jar preset", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": previewRoute(() => {}),
      "POST /projects": () => ({ status: 201, body: { project: makeProject() } }),
    });
    renderWizard();
    const user = userEvent.setup();
    await startEmpty(user, "Java application");
    await cont(user);
    await nameIt(user, "Acme API");
    await openAdvanced(user);
    // The Java card leads and runs the server; Ruby is off among the tools.
    expect(screen.getByRole("checkbox", { name: /Enable Java/ })).toBeChecked();
    await openTools(user);
    expect(screen.getByRole("checkbox", { name: /Enable Ruby/ })).not.toBeChecked();
    await user.selectOptions(screen.getByLabelText("Framework preset"), "jar");
    await user.type(screen.getByLabelText("Jar"), "target/api.jar");
    await toCreate(user);
    expect(screen.queryByLabelText(/Create starter/)).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    const body = create?.body as Record<string, unknown>;
    expect(body.php).toBeUndefined();
    expect(body.ruby).toBeUndefined();
    expect(body.createStarter).toBe(false);
    expect(body.java).toMatchObject({ version: "25", server: true, preset: "jar", jar: "target/api.jar", port: 8080 });
  });

  it("creates a Java template project with Gradle", async () => {
    const templates = [{ id: "spring-boot", name: "Spring Boot", description: "", runtime: "java" as const, java: { server: true, preset: "spring-boot", port: 8080 }, docroot: "", requiresDatabase: false, recommendedDatabase: "postgresql", buildTools: ["maven", "gradle"] }];
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: { ...runtimesFixture, templates } }),
      "POST /projects/preview": previewRoute(() => {}),
      "POST /projects": () => ({ status: 201, body: { project: makeProject() } }),
    });
    renderWizard();
    const user = userEvent.setup();
    await pickTemplate(user, /^Spring Boot/);
    await cont(user);
    await nameIt(user, "Acme API");
    await openAdvanced(user);
    expect(screen.getByLabelText("Build tool")).toHaveValue("maven");
    await user.selectOptions(screen.getByLabelText("Build tool"), "gradle");
    await toCreate(user);
    await user.click(await screen.findByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const body = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"))?.body as Record<string, unknown>;
    expect(body.template).toBe("spring-boot");
    expect(body.templateBuildTool).toBe("gradle");
  });

  it("creates a .NET project from a template: server on, no php key, no starter", async () => {
    const templates = [{ id: "aspnet-webapi", name: "ASP.NET Core Web API", description: "", runtime: "dotnet" as const, dotnet: { server: true, preset: "aspnetcore", port: 8080 }, docroot: "", requiresDatabase: false, recommendedDatabase: "postgresql" }];
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: { ...runtimesFixture, templates } }),
      "POST /projects/preview": previewRoute(() => {}),
      "POST /projects": () => ({ status: 201, body: { project: makeProject() } }),
    });
    renderWizard();
    const user = userEvent.setup();
    await pickTemplate(user, /^ASP\.NET Core Web API/);
    await cont(user);
    await nameIt(user, "Acme API");
    await openAdvanced(user);
    // The .NET card leads and runs the server; Java is off among the tools.
    expect(screen.getByRole("checkbox", { name: /Enable \.NET/ })).toBeChecked();
    await openTools(user);
    expect(screen.getByRole("checkbox", { name: /Enable Java/ })).not.toBeChecked();
    await user.selectOptions(screen.getByLabelText(".NET version"), "8");
    await toCreate(user);
    expect(screen.queryByLabelText(/Create starter/)).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    const body = create?.body as Record<string, unknown>;
    expect(body.php).toBeUndefined();
    expect(body.java).toBeUndefined();
    expect(body.createStarter).toBe(false);
    expect(body.template).toBe("aspnet-webapi");
    expect(body.dotnet).toMatchObject({ version: "8", server: true, mode: "dev", preset: "aspnetcore", project: "", dll: "", port: 8080 });
  });
});
