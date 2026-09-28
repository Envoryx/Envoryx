import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BranchesTab } from "./BranchesTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const envId = "9a8b7c6d-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const settings = { publicHost: "", baseDomain: "test", forceHttps: false, proxy: { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true } };

function parentProject() {
  const p = makeProject();
  return { ...p, git: { ...p.git, url: "https://git.example.com/shop.git", branch: "main" } };
}

function environment() {
  const p = makeProject();
  return {
    ...p,
    id: envId,
    name: "Acme Shop (feature/login)",
    slug: "acme-shop-feature-login",
    hostnames: ["acme-shop-feature-login.test"],
    parentId: id,
    git: { ...p.git, url: "https://git.example.com/shop.git", branch: "feature/login" },
    branchState: { commit: "c0ffee1234567890", deployStatus: "failed" as const, deployedAt: "2026-09-28T10:00:00Z", deployOutput: "$ php artisan migrate\nSQLSTATE: table exists" },
  };
}

describe("BranchesTab", () => {
  it("lists the environments, creates one from a remote branch and saves the settings", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings }),
      // Prefix matching: the longer path goes first.
      [`GET /projects/${id}/branches/remote`]: () => ({
        body: { branches: [{ name: "feature/login", commit: "c0ffee1234567890", environment: "acme-shop-feature-login", matches: false }, { name: "feature/pay", commit: "beefbeef00000000", matches: false }, { name: "main", commit: "aaaa", matches: false }] },
      }),
      [`GET /projects/${id}/branches`]: () => ({ body: { settings: { deploy: ["composer install"] }, environments: [environment()] } }),
      [`POST /projects/${id}/branches`]: () => ({ status: 201, body: { project: { ...environment(), name: "Acme Shop (feature/pay)" } } }),
      [`PUT /projects/${id}/branches/settings`]: (_u, init) => ({ body: { settings: JSON.parse(init.body as string) } }),
    });
    renderApp(<BranchesTab project={parentProject()} />);
    const user = userEvent.setup();
    const row = (await screen.findByRole("link", { name: "feature/login" })).closest("li") as HTMLElement;
    expect(within(row).getByText("deploy failed")).toBeInTheDocument();
    expect(within(row).getByText("c0ffee12")).toBeInTheDocument();
    await user.click(within(row).getByRole("button", { name: "Output" }));
    expect(within(row).getByText(/table exists/)).toBeInTheDocument();

    // Only branches without an environment are offered, the parent's own branch first skipped.
    await user.click(screen.getByRole("button", { name: "Load the branches of the repository" }));
    const select = await screen.findByLabelText("Branch");
    expect(select).toHaveValue("feature/pay");
    expect(within(select).queryByText(/feature\/login/)).not.toBeInTheDocument();
    expect(within(select).queryByText(/^main/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create environment" }));
    await screen.findByText("Acme Shop (feature/pay) is ready.");
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ branch: "feature/pay" });

    await user.click(screen.getByRole("checkbox", { name: /Watch the repository/ }));
    await user.type(screen.getByLabelText("Branches with an automatic environment"), "feature/*");
    await user.type(screen.getByLabelText("Stop idle environments after (days)"), "7");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({ watch: true, patterns: ["feature/*"], pollMinutes: 0, idleStopDays: 7, maxEnvironments: 0, deploy: ["composer install"] });
  });

  it("asks for a repository first", () => {
    mockApi({ ...authedRoutes });
    renderApp(<BranchesTab project={makeProject()} />);
    expect(screen.getByText(/Set a repository in the Git tab first/)).toBeInTheDocument();
  });

  it("deploys a branch environment and names its parent", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}`]: () => ({ body: { project: { ...parentProject(), branches: { deploy: ["composer install"] } } } }),
      [`POST /projects/${envId}/deploy`]: () => ({ body: { project: environment() } }),
    });
    renderApp(<BranchesTab project={environment()} />);
    const user = userEvent.setup();
    expect(await screen.findByRole("link", { name: "Acme Shop" })).toHaveAttribute("href", `/projects/${id}`);
    expect(await screen.findByText("composer install")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Pull and deploy" }));
    await screen.findByText("Deployed.");
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ pull: true });
  });
});
