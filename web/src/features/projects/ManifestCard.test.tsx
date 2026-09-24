import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ManifestCard } from "./ManifestCard";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const yaml = "version: 1\nname: shop\nphp:\n  version: \"8.4\"\n";

describe("ManifestCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("saves the manifest into a project directory that has none", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/manifest`]: () => ({ body: { fileName: "envoryx.yml", yaml, repository: { present: false } } }),
      [`PUT /projects/${id}/manifest/file`]: () => ({ body: { fileName: "envoryx.yml", yaml, repository: { present: true, plan: { changes: [], missingSecrets: [], inSync: true } } } }),
    });
    renderApp(<ManifestCard project={makeProject({ id })} />);
    const user = userEvent.setup();

    expect(await screen.findByText("The project directory has no envoryx.yml yet.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show the project as envoryx.yml" }));
    expect(screen.getByText(/version: "8.4"/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save to project directory" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT" && c.url.includes("/manifest/file"))).toBe(true));
    expect(await screen.findByText(/Commit it with the code/)).toBeInTheDocument();
  });

  it("lists the differences and applies them, removals only when asked", async () => {
    const plan = {
      changes: [
        { section: "php", action: "change", from: "version: 8.3", to: "version: 8.4" },
        { section: "redis", action: "remove", from: "version: 8", skipped: "prune" },
        { section: "env", item: "APP_DEBUG", action: "add", to: "1" },
      ],
      missingSecrets: [],
      inSync: false,
    };
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/manifest`]: () => ({ body: { fileName: "envoryx.yml", yaml, repository: { present: true, plan } } }),
      [`POST /projects/${id}/manifest/apply`]: () => ({ body: { plan: { ...plan, missingSecrets: ["STRIPE_SECRET"] }, project: makeProject({ id }) } }),
    });
    renderApp(<ManifestCard project={makeProject({ id })} />);
    const user = userEvent.setup();

    expect(await screen.findByText("differs from the project")).toBeInTheDocument();
    expect(screen.getByText("version: 8.3 → version: 8.4")).toBeInTheDocument();
    expect(screen.getByText("kept – removing needs the option below")).toBeInTheDocument();
    expect(screen.getByText("APP_DEBUG")).toBeInTheDocument();

    await user.click(screen.getByRole("checkbox", { name: /Also remove what the manifest no longer has/ }));
    await user.click(screen.getByRole("button", { name: "Apply to project" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.includes("/manifest/apply"))).toBe(true));
    expect(api.calls.find((c) => c.url.includes("/manifest/apply"))!.body).toEqual({ prune: true, start: true });
    expect(await screen.findByText("Applied. Still without a value: STRIPE_SECRET")).toBeInTheDocument();
  });

  it("reports a broken file", async () => {
    mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/manifest`]: () => ({ body: { fileName: "envoryx.yml", yaml, repository: { present: true, error: "invalid input: envoryx.yml: line 2: unknown key redsi" } } }),
    });
    renderApp(<ManifestCard project={makeProject({ id })} />);
    expect(await screen.findByText("The envoryx.yml in the project directory cannot be used")).toBeInTheDocument();
    expect(screen.getByText(/unknown key redsi/)).toBeInTheDocument();
  });
});
