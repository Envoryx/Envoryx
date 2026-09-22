import { expect, test, type Page } from "@playwright/test";
import { adminPassword, adminUsername, nodeProjectName, nodeProjectSlug } from "./global-setup";

// A project without PHP: the wizard's "Static site" stack creates the web container alone,
// which serves the starter index.html on the published port. This is the cheapest shape
// without PHP (no npm install in CI) and exercises every code path that used to assume a PHP
// container – starter page, git one-shot image, IDE tab. Runs after lifecycle.spec.ts, which
// created the administrator account; the tests build on each other, hence serial.
test.describe.configure({ mode: "serial" });

const starterText = "Your Envoryx project is served by the web server";

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("Username").fill(adminUsername);
  await page.getByLabel("Password").fill(adminPassword);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL("/");
}

async function openProject(page: Page) {
  await page.goto("/projects");
  await page.getByRole("link", { name: nodeProjectName }).click();
  await expect(page.getByRole("heading", { name: nodeProjectName })).toBeVisible();
}

/** Waits for the wizard to land on the detail page, or fails fast with the wizard's own error. */
async function waitForCreated(page: Page) {
  await expect(page.getByTestId("create-progress")).toBeVisible();
  const failed = page.getByText("Creation failed");
  await Promise.race([
    expect(page).toHaveURL(/\/projects\/(?!new$)[^/]+$/, { timeout: 9 * 60_000 }),
    failed.waitFor({ timeout: 9 * 60_000 }).then(async () => {
      throw new Error(`project creation failed: ${await page.getByRole("alert").innerText()}`);
    }),
  ]);
}

test("the wizard creates a static project without PHP that the web server serves", async ({ page, request }) => {
  // Creating pulls the Caddy image on a fresh host (the PHP image is not needed).
  test.setTimeout(10 * 60_000);
  await signIn(page);
  await page.goto("/projects/new");

  const cont = () => page.getByRole("button", { name: "Continue" }).click();
  await page.getByLabel("Project name").fill(nodeProjectName);
  await expect(page.getByText(`Identifier: ${nodeProjectSlug}`)).toBeVisible();
  // The runtime radios carry their name as accessible label.
  await expect(page.getByRole("radio", { name: "PHP application" })).toBeChecked();
  await page.getByRole("radio", { name: "Static site" }).check();
  await expect(page.getByRole("radio", { name: "Static site" })).toBeChecked();
  // Templates need a runtime; only "Blank" stays offered for a static site.
  await expect(page.getByRole("radio", { name: /Blank/ })).toBeChecked();
  await expect(page.getByRole("radio", { name: /WordPress/ })).toHaveCount(0);
  await cont();

  // Runtimes: the static stack ticks neither PHP nor Node.js.
  await expect(page.getByLabel("Enable PHP")).not.toBeChecked();
  await expect(page.getByLabel("Enable Node.js")).not.toBeChecked();
  await expect(page.getByLabel("PHP version")).toHaveCount(0);
  await cont();
  await expect(page.getByLabel("Web server")).toHaveValue("caddy");
  // The SPA fallback exists only for projects without PHP; keep it off here.
  await expect(page.getByLabel("SPA fallback to index.html")).not.toBeChecked();
  await cont();
  await expect(page.getByRole("radiogroup", { name: "Database" })).toBeVisible();
  await cont();
  await page.getByRole("button", { name: "Add variable" }).waitFor();
  await cont();

  // Preview: the plan has a web container and nothing else; the starter is an index.html.
  await expect(page.getByText(`envoryx-${nodeProjectSlug}-web`)).toBeVisible();
  await expect(page.getByText(`envoryx-${nodeProjectSlug}-php`)).toHaveCount(0);
  await expect(page.getByText(`envoryx-${nodeProjectSlug}-node`)).toHaveCount(0);
  await expect(page.getByLabel("Create starter index.html")).toBeChecked();
  await expect(page.getByLabel("Start project after creation")).toBeChecked();

  await page.getByRole("button", { name: "Create project" }).click();
  await waitForCreated(page);
  await expect(page.getByRole("heading", { name: nodeProjectName })).toBeVisible();
  await expect(page.getByText("Running", { exact: true })).toBeVisible({ timeout: 60_000 });
  await expect(page.getByTestId("operation").filter({ hasText: `${nodeProjectName} created` })).toBeVisible();

  // Without a dev server the web container's port is published and serves the starter page.
  const link = page.getByRole("link", { name: /^http:\/\/127\.0\.0\.1:\d+/ });
  const url = await link.getAttribute("href");
  expect(url).toMatch(/^http:\/\/127\.0\.0\.1:\d+$/);
  await expect
    .poll(async () => (await request.get(url!, { failOnStatusCode: false })).status(), { timeout: 30_000 })
    .toBe(200);
  const body = await (await request.get(url!)).text();
  expect(body).toContain(starterText);
  expect(body).toContain(`<h1>${nodeProjectSlug}</h1>`);
  // The static config denies dotfiles; a missing page is a plain 404, not the PHP front controller.
  expect((await request.get(`${url}/.env`, { failOnStatusCode: false })).status()).not.toBe(200);
  expect((await request.get(`${url}/no-such-page`, { failOnStatusCode: false })).status()).toBe(404);

  // The API classifies the project: it serves static files and has no application container.
  const list = await page.request.get("/api/v1/projects");
  const { projects } = (await list.json()) as { projects: { slug: string; serves: string; services: { kind: string }[] }[] };
  const project = projects.find((p) => p.slug === nodeProjectSlug);
  expect(project?.serves).toBe("static");
  expect(project?.services.map((s) => s.kind)).toEqual(["web"]);
});

test("the tabs work without a PHP container", async ({ page }) => {
  await signIn(page);
  await openProject(page);

  // Logs: the web server is the first (and only) service, so its tab is selected.
  await page.getByRole("tab", { name: "Logs" }).click();
  await expect(page.getByRole("tab", { name: /Caddy/ })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("live", { exact: true })).toBeVisible();

  // Git: the deploy key and repository form render; no "needs PHP" refusal.
  await page.getByRole("tab", { name: "Git" }).click();
  await expect(page.getByLabel("Repository URL")).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);

  // IDE: no Xdebug card and no PHP path for a project without PHP.
  await page.getByRole("tab", { name: "IDE" }).click();
  await expect(page.getByText("Remote interpreter (SSH)")).toBeVisible();
  await expect(page.getByText("Xdebug")).toHaveCount(0);
  await expect(page.getByText("PHP path")).toHaveCount(0);
});

test("stop and delete the static project", async ({ page }) => {
  await signIn(page);
  await openProject(page);
  await page.getByRole("button", { name: "Stop", exact: true }).click();
  await expect(page.getByText("Stopped", { exact: true })).toBeVisible({ timeout: 60_000 });

  await page.getByRole("button", { name: "Delete project" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel(`Type ${nodeProjectSlug} to confirm`).fill(nodeProjectSlug);
  await dialog.getByLabel("Also delete project files").check();
  await dialog.getByRole("button", { name: "Delete project" }).click();
  await expect(page).toHaveURL("/projects", { timeout: 60_000 });
  await expect(page.getByTestId("operation").filter({ hasText: `${nodeProjectName} deleted` })).toBeVisible();
});

// Manual smoke for the Node templates (DEVELOPMENT.md → "Node image and scaffold smoke"):
// scaffolds Vite + React through npm against the registry and waits for the dev server to
// answer on the node container's host port. Needs network access and a few minutes, so it is
// skipped by default; remove the skip locally when touching the Node templates or the image.
test.skip("the Vite template scaffolds a Node.js project whose dev server answers", async ({ page, request }) => {
  test.setTimeout(15 * 60_000);
  await signIn(page);
  await page.goto("/projects/new");

  const cont = () => page.getByRole("button", { name: "Continue" }).click();
  await page.getByLabel("Project name").fill(nodeProjectName);
  await page.getByRole("radio", { name: "Node.js application" }).check();
  await page.getByRole("radio", { name: /Vite \+ React/ }).check();
  await cont();
  await expect(page.getByLabel("Enable Node.js")).toBeChecked();
  await expect(page.getByLabel("Enable PHP")).not.toBeChecked();
  await expect(page.getByRole("checkbox", { name: /^Run a dev server/ })).toBeChecked();
  await cont();
  await cont();
  await cont();
  await cont();
  await expect(page.getByText(`envoryx-${nodeProjectSlug}-node`)).toBeVisible();
  await expect(page.getByText("Node dev server (the HTTP port stays unpublished)")).toBeVisible();
  await expect(page.getByLabel("Create starter index.html")).toHaveCount(0);
  await page.getByRole("button", { name: "Create project" }).click();
  await waitForCreated(page);
  await expect(page.getByText("Running", { exact: true })).toBeVisible({ timeout: 60_000 });

  // The header links to the node container's host port; Vite answers there once compiled.
  const url = await page.getByRole("link", { name: /^http:\/\/127\.0\.0\.1:\d+/ }).getAttribute("href");
  await expect
    .poll(async () => (await request.get(url!, { failOnStatusCode: false }).catch(() => null))?.status() ?? 0, { timeout: 5 * 60_000 })
    .toBe(200);
  expect(await (await request.get(url!)).text()).toContain("/@vite/client");

  await page.getByRole("tab", { name: "Logs" }).click();
  await expect(page.getByText(/VITE v/)).toBeVisible({ timeout: 60_000 });
});
