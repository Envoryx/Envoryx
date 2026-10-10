import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ImportSiteCard } from "./ImportSiteCard";
import { RestoreNewDialog } from "./RestoreNewDialog";
import { api as client } from "@/api/client";
import { DeletedProjectBackupsCard } from "@/features/settings/DeletedProjectBackupsCard";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";
import type { BackupInfo } from "@/api/types";

const pid = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const backup: BackupInfo = {
  id: "9a8b7c6d-1a2b-4c3d-8e9f-0a1b2c3d4e5f",
  dir: "20261010-010203-abcdef01",
  kind: "full",
  sizeBytes: 2048,
  createdAt: "2026-10-09T10:00:00Z",
  missing: false,
  meta: { format: 1, envoryx: "0.24.0", projectId: pid, projectName: "Acme Shop", slug: "acme-shop", createdAt: "2026-10-09T10:00:00Z", runtimes: {}, database: { type: "mariadb", version: "11", name: "acme_shop", bytes: 1024 }, files: { bytes: 1024, entries: 3, includeDependencies: false } },
} as unknown as BackupInfo;

describe("RestoreNewDialog", () => {
  it("restores everything the backup holds under a suggested name", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`POST /backups/${pid}/${backup.id}/restore-new`]: () => ({ status: 201, body: { project: makeProject({ id: "new-id", name: "Acme Shop Restored", slug: "acme-shop-restored" }) } }),
    });
    renderApp(<RestoreNewDialog projectId={pid} projectName="Acme Shop" backup={backup} onClose={() => {}} />);
    const user = userEvent.setup();

    expect(await screen.findByPlaceholderText("Acme Shop Restored")).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: /Restore object storage/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: /Restore database/ }));
    await user.click(screen.getByRole("button", { name: "Restore into a new project" }));

    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/restore-new"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/restore-new"))!.body).toEqual({ name: "Acme Shop Restored", database: false, files: true, storage: false, start: false });
  });
});

describe("DeletedProjectBackupsCard", () => {
  it("lists the backups of deleted projects and restores one into a new project", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /backups/orphaned": () => ({ body: { backups: [{ projectId: pid, projectName: "Old Blog", slug: "old-blog", backup }] } }),
      [`POST /backups/${pid}/${backup.id}/restore-new`]: () => ({ status: 201, body: { project: makeProject({ id: "new-id", name: "Old Blog" }) } }),
    });
    renderApp(<DeletedProjectBackupsCard />);
    const user = userEvent.setup();

    expect(await screen.findByText("Old Blog")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Restore into a new project" }));
    const name = await screen.findByLabelText("Name of the new project");
    await user.type(name, "Old Blog");
    await user.click(screen.getAllByRole("button", { name: "Restore into a new project" }).at(-1)!);
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/restore-new"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/restore-new"))!.body).toMatchObject({ name: "Old Blog", database: true, files: true });
  });
});

describe("DeletedProjectBackupsCard offsite", () => {
  it("fetches a project backup from an offsite target and opens the restore", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /backups/orphaned": () => ({ body: { backups: [] } }),
      "GET /offsite/targets/t1/projects": () => ({
        body: { projects: [{ slug: "old-blog", backups: [{ key: "projects/old-blog/20261010-010203-abcdef01.full.manual.tar.age", id: "20261010-010203-abcdef01", createdAt: "2026-10-10T01:02:03Z", kind: "full", source: "manual", sizeBytes: 4096, encrypted: true }] }] },
      }),
      "POST /offsite/targets/t1/projects/fetch": () => ({ status: 201, body: { backup: { projectId: pid, projectName: "Old Blog", slug: "old-blog", backup, projectExists: false } } }),
      // Routes match by prefix, so the target list comes after the routes it is a prefix of.
      "GET /offsite": () => ({ body: { targets: [{ id: "t1", name: "Backblaze", type: "s3", enabled: true }] } }),
    });
    renderApp(<DeletedProjectBackupsCard />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Show projects on Backblaze" }));
    expect(await screen.findByText("old-blog")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Restore into a new project" }));
    expect(await screen.findByLabelText("Name of the new project")).toBeInTheDocument();
    expect(api.calls.find((c) => c.url.endsWith("/projects/fetch"))!.body).toEqual({ key: "projects/old-blog/20261010-010203-abcdef01.full.manual.tar.age" });
  });
});

describe("ImportSiteCard", () => {
  it("restores an uploaded Envoryx backup into a new project instead of analysing it", async () => {
    mockApi({
      ...authedRoutes,
      [`POST /backups/${pid}/${backup.id}/restore-new`]: () => ({ status: 201, body: { project: makeProject({ id: "new-id", name: "Acme Shop" }) } }),
    });
    const upload = vi.spyOn(client.siteImports, "upload").mockResolvedValue({ backup: { projectId: pid, projectName: "Acme Shop", slug: "acme-shop", backup, projectExists: false } });
    const onUploaded = vi.fn();
    renderApp(<ImportSiteCard value={null} onUploaded={onUploaded} onDiscard={() => {}} adaptConfig onAdaptConfig={() => {}} />);
    const user = userEvent.setup();

    await user.upload(await screen.findByLabelText("Website archive"), new File(["tar"], "acme-shop-20261010-010203-abcdef01.tar"));
    await user.click(screen.getByRole("button", { name: "Upload and analyse" }));

    expect(await screen.findByText(/Recognised: a backup of Acme Shop/)).toBeInTheDocument();
    expect(await screen.findByLabelText("Name of the new project")).toBeInTheDocument();
    expect(onUploaded).not.toHaveBeenCalled();
    upload.mockRestore();
  });
});
