import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BackupsTab } from "./BackupsTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const backup = {
  id: "b1", dir: "20260918-100000-abcd1234", kind: "full", sizeBytes: 2048, createdAt: "2026-09-18T10:00:00Z", missing: false,
  meta: { format: 1, staqio: "dev", projectId: id, projectName: "Shimly API", slug: "shimly-api", createdAt: "2026-09-18T10:00:00Z", note: "before deploy", database: { type: "mariadb", version: "11", name: "shimly_api", bytes: 1024 }, files: { bytes: 1024, entries: 12, includeDependencies: false }, runtimes: {} },
};

describe("BackupsTab", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("creates a backup and requires typed confirmation to restore", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/backups`]: () => ({ body: { backups: [backup] } }),
      [`POST /projects/${id}/backups/b1/restore`]: () => ({ body: { backup } }),
      [`POST /projects/${id}/backups`]: () => ({ status: 201, body: { backup } }),
    });
    const project = makeProject({ services: [...makeProject().services, { kind: "database", variant: "mariadb", version: "11", image: "mariadb:11", enabled: true, config: {} }] });
    renderApp(<BackupsTab project={project} />);
    const user = userEvent.setup();

    expect(await screen.findByText(/before deploy/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create backup" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith("/backups"))).toBe(true));
    expect(api.calls.find((c) => c.method === "POST" && c.url.endsWith("/backups"))!.body).toEqual({ database: true, files: true, includeDependencies: false, note: "" });

    await user.click(screen.getByRole("button", { name: "Restore" }));
    expect(await screen.findByText(/This overwrites current data/)).toBeInTheDocument();
    const dialogButton = () => screen.getAllByRole("button", { name: "Restore" }).at(-1) as HTMLButtonElement;
    expect(dialogButton()).toBeDisabled();
    await user.type(screen.getByLabelText("Type shimly-api to confirm"), "shimly-api");
    await waitFor(() => expect(dialogButton()).not.toBeDisabled());
    await user.click(dialogButton());
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/restore"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/restore"))!.body).toEqual({ database: true, files: true, wipeFiles: false, confirm: "shimly-api" });
  });
});
