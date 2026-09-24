import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { OffsiteTargetsCard } from "./OffsiteTargetsCard";
import { BackupsTab } from "@/features/projects/BackupsTab";
import { InstanceBackupsCard } from "@/features/settings/InstanceBackupsCard";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const stored = {
  id: "t1", name: "Backblaze", type: "s3", enabled: true, auto: true, instance: true, instanceHour: 3, prefix: "envoryx", keep: 14, instanceKeep: 14, encrypt: true,
  endpoint: "https://s3.eu-central-003.backblazeb2.com", region: "eu-central-003", bucket: "acme", accessKey: "key-id",
  secrets: { passphrase: true, secretKey: true, password: false, privateKey: false }, location: "https://s3.eu-central-003.backblazeb2.com/acme/envoryx",
  last: { targetId: "t1", targetName: "Backblaze", backupId: "b0", scope: "project", status: "failed", error: "connection refused", sizeBytes: 0, attempts: 1, updatedAt: "2026-09-24T03:00:00Z" },
};
const names = [{ id: "t1", name: "Backblaze", type: "s3", enabled: true, encrypt: true }];

describe("OffsiteTargetsCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("adds an SFTP target after a test that records the server key", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /offsite": () => ({ body: { targets: [] } }),
      "POST /offsite/test": () => ({ body: { result: { hostKey: "SHA256:abcdef" } } }),
      "POST /offsite/targets": (_u, init) => ({ status: 201, body: { target: { ...JSON.parse(init.body as string), id: "t2", secrets: {}, location: "sftp://u1@box:23/envoryx" } } }),
    });
    renderApp(<OffsiteTargetsCard />);
    const user = userEvent.setup();
    expect(await screen.findByText(/No offsite target yet/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Add target" }));
    const dialog = screen.getByRole("dialog");
    await user.type(within(dialog).getByLabelText("Name"), "Storage Box");
    await user.selectOptions(within(dialog).getByLabelText("Type"), "sftp");
    await user.type(within(dialog).getByLabelText("Host"), "u1.your-storagebox.de");
    await user.clear(within(dialog).getByLabelText("Port"));
    await user.type(within(dialog).getByLabelText("Port"), "23");
    await user.type(within(dialog).getByLabelText("User"), "u1");
    await user.type(within(dialog).getByLabelText("Password"), "pw");
    await user.type(within(dialog).getByLabelText("Passphrase"), "correct horse battery");
    await user.click(within(dialog).getByRole("button", { name: "Test" }));
    expect(await within(dialog).findByText(/Connection works/)).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Server key")).toHaveValue("SHA256:abcdef");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith("/offsite/targets"))).toBe(true));
    const body = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/offsite/targets"))!.body as Record<string, unknown>;
    expect(body).toMatchObject({ name: "Storage Box", type: "sftp", host: "u1.your-storagebox.de", port: 23, user: "u1", password: "pw", hostKey: "SHA256:abcdef", encrypt: true, passphrase: "correct horse battery", auto: true, instance: true });
  });

  it("lists targets with their last result and keeps stored secrets on edit", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /offsite": () => ({ body: { targets: [stored] } }),
      "PUT /offsite/targets/t1": (_u, init) => ({ body: { target: { ...stored, ...JSON.parse(init.body as string) } } }),
    });
    renderApp(<OffsiteTargetsCard />);
    const user = userEvent.setup();
    expect(await screen.findByText("Backblaze")).toBeInTheDocument();
    expect(screen.getByText(/connection refused/)).toBeInTheDocument();
    expect(screen.getByText("instance daily 03:00")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByLabelText("Secret key")).toHaveValue("");
    await user.clear(within(dialog).getByLabelText("Keep per project"));
    await user.type(within(dialog).getByLabelText("Keep per project"), "30");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    const body = api.calls.find((c) => c.method === "PUT")!.body as Record<string, unknown>;
    expect(body.keep).toBe(30);
    expect(body.secretKey ?? "").toBe("");
    expect("secrets" in body).toBe(false);
  });
});

describe("offsite copies of project backups", () => {
  afterEach(() => vi.unstubAllGlobals());

  const backup = {
    id: "b1", dir: "20260918-100000-abcd1234", kind: "files", sizeBytes: 2048, createdAt: "2026-09-18T10:00:00Z", missing: false,
    meta: { format: 1, envoryx: "dev", projectId: id, projectName: "Acme Shop", slug: "acme-shop", createdAt: "2026-09-18T10:00:00Z", files: { bytes: 1024, entries: 12, includeDependencies: false }, runtimes: {} },
  };

  it("shows the copies, copies on request and fetches one back", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`POST /projects/${id}/backups/b1/offsite`]: () => ({ status: 202, body: { offsite: [] } }),
      [`GET /projects/${id}/backups`]: () => ({ body: { backups: [backup], offsite: {}, offsiteTargets: names } }),
      [`GET /projects/${id}/offsite/t1`]: () => ({
        body: {
          backups: [
            { key: "projects/acme-shop/20260918-100000-abcd1234.files.manual.tar.age", id: "20260918-100000-abcd1234", createdAt: "2026-09-18T10:00:00Z", kind: "files", source: "manual", sizeBytes: 3000, encrypted: true },
            { key: "projects/acme-shop/20260910-020000-eeee0000.full.scheduled.tar.age", id: "20260910-020000-eeee0000", createdAt: "2026-09-10T02:00:00Z", kind: "full", source: "scheduled", sizeBytes: 9000, encrypted: true },
          ],
        },
      }),
      [`POST /projects/${id}/offsite/t1/fetch`]: () => ({ status: 201, body: { backup: { ...backup, id: "b2", createdAt: "2026-09-10T02:00:00Z" } } }),
    });
    renderApp(<BackupsTab project={makeProject({ id })} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Copy offsite" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/backups/b1/offsite"))).toBe(true));

    await user.click(screen.getByRole("button", { name: "Show copies on Backblaze" }));
    expect(await screen.findByText("also here")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Fetch" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/offsite/t1/fetch"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/offsite/t1/fetch"))!.body).toEqual({ key: "projects/acme-shop/20260910-020000-eeee0000.full.scheduled.tar.age" });
    expect(await screen.findByText(/is back in the list above/)).toBeInTheDocument();
  });

  it("offers the offsite copy when creating and shows a failed upload", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/backups`]: () => ({
        body: { backups: [backup], offsiteTargets: names, offsite: { b1: [{ targetId: "t1", targetName: "Backblaze", backupId: "b1", scope: "project", status: "failed", error: "HTTP 403", sizeBytes: 0, attempts: 5, updatedAt: "2026-09-24T03:00:00Z" }] } },
      }),
      [`POST /projects/${id}/backups`]: () => ({ status: 201, body: { backup, offsite: [{ targetId: "t1", targetName: "Backblaze", status: "pending" }] } }),
    });
    renderApp(<BackupsTab project={makeProject({ id })} />);
    const user = userEvent.setup();
    expect(await screen.findByText(/Backblaze · failed/)).toBeInTheDocument();
    await user.click(screen.getByLabelText(/Also copy offsite/));
    await user.click(screen.getByRole("button", { name: "Create backup" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith("/backups"))).toBe(true));
    expect(api.calls.find((c) => c.method === "POST" && c.url.endsWith("/backups"))!.body).toMatchObject({ offsite: true });
    expect(await screen.findByText(/the offsite copy is on its way/)).toBeInTheDocument();
  });
});

describe("disaster recovery from an offsite target", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("fetches an instance backup from the target", async () => {
    const meta = { format: 1, envoryx: "0.7.0", schema: 10, createdAt: "2026-09-23T03:00:00Z", kind: "scheduled", entries: 12 };
    const api = mockApi({
      ...authedRoutes,
      "GET /instance/backups": () => ({ body: { backups: [], pendingRestore: null, dir: "/backups/_instance", canRestart: true, offsite: {}, offsiteTargets: names } }),
      "GET /offsite/targets/t1/instance": () => ({
        body: { backups: [{ key: "instance/scheduled-20260923-030000-ab12.tar.gz.age", id: "scheduled-20260923-030000-ab12", createdAt: "2026-09-23T03:00:00Z", kind: "scheduled", source: "scheduled", sizeBytes: 40000, encrypted: true }] },
      }),
      "POST /offsite/targets/t1/instance/fetch": () => ({ status: 201, body: { backup: { id: "upload-20260924-100000-cd34", kind: "upload", sizeBytes: 40000, createdAt: "2026-09-23T03:00:00Z", meta } } }),
    });
    renderApp(<InstanceBackupsCard />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Show copies on Backblaze" }));
    await user.click(await screen.findByRole("button", { name: "Fetch" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/instance/fetch"))).toBe(true));
    expect(await screen.findByText(/Fetched as upload-20260924-100000-cd34/)).toBeInTheDocument();
  });
});
