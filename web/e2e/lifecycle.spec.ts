import { existsSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { adminPassword, adminUsername, projectName, projectSlug } from "./global-setup";

// The whole life of a project, in the order a new user goes through it: first-run setup,
// sign-in, the wizard, a running project answering HTTP, live logs, stop and delete.
// The specs share one Envoryx instance and depend on each other, hence serial.
test.describe.configure({ mode: "serial" });

const username = adminUsername;
const password = adminPassword;
const starterText = "Your Envoryx project is served by PHP";

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("Username").fill(username);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL("/");
}

/** Opens the project's detail page from the list. */
async function openProject(page: Page) {
  await page.goto("/projects");
  await page.getByRole("link", { name: projectName }).click();
  await expect(page.getByRole("heading", { name: projectName })).toBeVisible();
}

test("first visit asks for the administrator account", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL("/setup");
  await expect(page.getByRole("heading", { name: "Welcome to Envoryx" })).toBeVisible();

  await page.getByLabel("Username").fill(username);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel("Confirm password").fill("something else");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByText("Passwords do not match.")).toBeVisible();

  await page.getByLabel("Confirm password").fill(password);
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL("/");
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
});

test("sign out, reject a wrong password, sign in", async ({ page }) => {
  await signIn(page);
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL("/login");

  // Protected pages redirect to the sign-in page while signed out.
  await page.goto("/projects");
  await expect(page).toHaveURL("/login");

  await page.getByLabel("Username").fill(username);
  await page.getByLabel("Password").fill("wrong-password-1");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(page).toHaveURL("/login");

  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  // Back where the user wanted to go.
  await expect(page).toHaveURL("/projects");
  await expect(page.getByText("No projects yet")).toBeVisible();
});

test("the wizard creates a project that starts and serves its starter page", async ({ page, request }) => {
  // Creating pulls the PHP and Caddy images on a fresh host.
  test.setTimeout(10 * 60_000);
  await signIn(page);
  await page.goto("/projects");
  await page.getByRole("link", { name: "Create your first project" }).click();
  await expect(page).toHaveURL("/projects/new");

  const cont = () => page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("button", { name: "Continue" })).toBeDisabled();
  await page.getByLabel("Project name").fill(projectName);
  await expect(page.getByText(`Identifier: ${projectSlug}`)).toBeVisible();
  // The PHP stack is preselected; runtimes, web server, database & services and
  // environment keep their defaults.
  await expect(page.getByRole("radio", { name: "PHP application" })).toBeChecked();
  await cont();
  await expect(page.getByLabel("PHP version")).toHaveValue(/./);
  await cont();
  await expect(page.getByLabel("Web server")).toHaveValue("caddy");
  await cont();
  await expect(page.getByRole("radiogroup", { name: "Database" })).toBeVisible();
  await cont();
  await page.getByRole("button", { name: "Add variable" }).waitFor();
  await cont();

  // Preview: the plan names the containers that are about to be created.
  await expect(page.getByText(`envoryx-${projectSlug}-php`)).toBeVisible();
  await expect(page.getByText(`envoryx-${projectSlug}-web`)).toBeVisible();
  await expect(page.getByLabel("Create starter index.php")).toBeChecked();
  await expect(page.getByLabel("Start project after creation")).toBeChecked();

  await page.getByRole("button", { name: "Create project" }).click();
  // While the server works, the wizard shows what it is doing.
  await expect(page.getByTestId("create-progress")).toBeVisible();
  // Either the detail page appears or the wizard reports why not – no point in waiting
  // the full pull timeout for a failure that is already on screen.
  const failed = page.getByText("Creation failed");
  await Promise.race([
    expect(page).toHaveURL(/\/projects\/(?!new$)[^/]+$/, { timeout: 9 * 60_000 }),
    failed.waitFor({ timeout: 9 * 60_000 }).then(async () => {
      throw new Error(`project creation failed: ${await page.getByRole("alert").innerText()}`);
    }),
  ]);
  await expect(page.getByRole("heading", { name: projectName })).toBeVisible();
  await expect(page.getByText("Running", { exact: true })).toBeVisible({ timeout: 60_000 });
  // The tray announces the outcome.
  await expect(page.getByTestId("operation").filter({ hasText: `${projectName} created` })).toBeVisible();

  // The header links to the published port; the starter page must answer there.
  const link = page.getByRole("link", { name: /^http:\/\/127\.0\.0\.1:\d+/ });
  const url = await link.getAttribute("href");
  expect(url).toMatch(/^http:\/\/127\.0\.0\.1:\d+$/);
  await expect
    .poll(async () => (await request.get(url!, { failOnStatusCode: false })).status(), { timeout: 30_000 })
    .toBe(200);
  const body = await (await request.get(url!)).text();
  expect(body).toContain(starterText);
  expect(body).toContain(`<h1>${projectSlug}</h1>`);
});

test("the logs tab streams container output", async ({ page }) => {
  await signIn(page);
  await openProject(page);
  await page.getByRole("tab", { name: "Logs" }).click();

  // PHP is the first service; PHP-FPM announces its start on stderr, so the tail is not empty.
  await expect(page.getByText("live", { exact: true })).toBeVisible();
  const rows = page.locator("table tbody tr");
  await expect(rows.first()).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText("ready to handle connections")).toBeVisible();

  // Filtering narrows the lines and the footer says so.
  await page.getByLabel("Search logs").fill("no-such-line-anywhere");
  await expect(page.getByText("No lines match the filter.")).toBeVisible();
  await page.getByLabel("Search logs").fill("");
  await expect(rows.first()).toBeVisible();

  // Switching to the web server keeps streaming.
  await page.getByRole("tab", { name: /Caddy/ }).click();
  await expect(page.getByText("live", { exact: true })).toBeVisible();
});

test("stop, then delete the project including its files", async ({ page, request }) => {
  await signIn(page);
  await openProject(page);
  const url = await page.getByRole("link", { name: /^http:\/\/127\.0\.0\.1:\d+/ }).getAttribute("href");

  await page.getByRole("button", { name: "Stop", exact: true }).click();
  await expect(page.getByText("Stopped", { exact: true })).toBeVisible({ timeout: 60_000 });
  await expect(page.getByTestId("operation").filter({ hasText: `${projectName} stopped` })).toBeVisible();
  await expect
    .poll(async () => (await request.get(url!, { failOnStatusCode: false }).catch(() => null))?.status() ?? 0, { timeout: 30_000 })
    .not.toBe(200);

  await page.getByRole("button", { name: "Delete project" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  const confirm = dialog.getByRole("button", { name: "Delete project" });
  await expect(confirm).toBeDisabled();
  await dialog.getByLabel(`Type ${projectSlug} to confirm`).fill("wrong");
  await expect(confirm).toBeDisabled();
  await dialog.getByLabel(`Type ${projectSlug} to confirm`).fill(projectSlug);
  await dialog.getByLabel("Also delete project files").check();
  await confirm.click();

  await expect(page).toHaveURL("/projects", { timeout: 60_000 });
  await expect(page.getByText("No projects yet")).toBeVisible();
  await expect(page.getByTestId("operation").filter({ hasText: `${projectName} deleted` })).toBeVisible();
  // Gone for good: the API no longer knows the project and its directory is removed.
  const list = await page.request.get("/api/v1/projects");
  expect(list.ok()).toBe(true);
  expect(JSON.stringify(await list.json())).not.toContain(projectSlug);
  expect(existsSync(join(process.env.ENVORYX_E2E_PROJECTS_DIR!, projectSlug))).toBe(false);
});
