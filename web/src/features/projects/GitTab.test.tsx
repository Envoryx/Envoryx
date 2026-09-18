import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { GitTab } from "./GitTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";

describe("GitTab", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("saves repository settings without sending an empty token and pulls", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings/deploy-key": () => ({ body: { publicKey: "ssh-ed25519 AAAA envoryx-deploy-key" } }),
      [`GET /projects/${id}/git`]: () => ({
        body: { git: { configured: true, url: "https://github.com/x/y.git", branch: "main", hasToken: true, isRepo: true, currentBranch: "main", shortHash: "abc123d", subject: "Init", author: "Stefan", date: "2026-09-18T10:00:00Z", dirty: 0, remote: "https://github.com/x/y.git" } },
      }),
      [`PUT /projects/${id}/git`]: () => ({ body: { git: { configured: true, hasToken: true, isRepo: true, dirty: 0 } } }),
      [`POST /projects/${id}/git/pull`]: () => ({ body: { result: { output: "Already up to date.", exitCode: 0, status: { configured: true, hasToken: true, isRepo: true, dirty: 0 } } } }),
    });
    const project = makeProject({ git: { url: "https://github.com/x/y.git", branch: "main", username: "", hasToken: true } });
    renderApp(<GitTab project={project} />);
    const user = userEvent.setup();

    expect(await screen.findByText("abc123d")).toBeInTheDocument();
    expect(screen.getByText("clean")).toBeInTheDocument();
    expect(screen.getByText("ssh-ed25519 AAAA envoryx-deploy-key")).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Branch"));
    await user.type(screen.getByLabelText("Branch"), "develop");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    const put = api.calls.find((c) => c.method === "PUT")!.body as Record<string, unknown>;
    expect(put).toEqual({ url: "https://github.com/x/y.git", branch: "develop", username: "" });
    expect("token" in put).toBe(false);

    await user.click(screen.getByRole("button", { name: "Pull" }));
    expect(await screen.findByText("Already up to date.")).toBeInTheDocument();
  });
});
