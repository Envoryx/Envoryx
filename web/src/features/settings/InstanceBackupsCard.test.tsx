import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { InstanceBackupsCard } from "./InstanceBackupsCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const meta = { format: 1, envoryx: "1.1.0", schema: 6, createdAt: "2026-09-18T10:00:00Z", kind: "manual", note: "before update", entries: 12 };

describe("InstanceBackupsCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("lists, creates, imports and schedules a restore with confirmation", async () => {
    let backups = [{ id: "manual-20260918-100000-ab12", kind: "manual", sizeBytes: 20480, createdAt: "2026-09-18T10:00:00Z", meta }];
    let pending: { id: string; requestedAt: string } | null = null;
    const api = mockApi({
      ...authedRoutes,
      "GET /instance/backups": () => ({ body: { backups, pendingRestore: pending, dir: "/backups/_instance", canRestart: true } }),
      "POST /instance/backups/upload": () => {
        const b = { id: "upload-20260919-090000-cd34", kind: "upload", sizeBytes: 30000, createdAt: "2026-09-19T09:00:00Z", meta: { ...meta, kind: "manual", envoryx: "1.0.0" } };
        backups = [b, ...backups];
        return { status: 201, body: { backup: b } };
      },
      "POST /instance/backups/manual-20260918-100000-ab12/restore": () => {
        pending = { id: "manual-20260918-100000-ab12", requestedAt: "2026-09-19T09:05:00Z" };
        return { status: 202, body: { scheduled: "manual-20260918-100000-ab12", restarting: true } };
      },
      "POST /instance/backups": () => {
        const b = { id: "manual-20260919-091000-ef56", kind: "manual", sizeBytes: 21000, createdAt: "2026-09-19T09:10:00Z", meta: { ...meta, note: "test" } };
        backups = [b, ...backups];
        return { status: 201, body: { backup: b } };
      },
    });
    renderApp(<InstanceBackupsCard />);
    const user = userEvent.setup();

    expect(await screen.findByText(/manual-20260918-100000-ab12/)).toBeInTheDocument();
    expect(screen.getByText(/before update/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Download/ })).toHaveAttribute("href", "/api/v1/instance/backups/manual-20260918-100000-ab12/download");

    await user.type(screen.getByLabelText("Note"), "test");
    await user.click(screen.getByRole("button", { name: "Create backup" }));
    expect(await screen.findByText(/Backup manual-20260919-091000-ef56 created/)).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "POST" && c.url.endsWith("/instance/backups"))!.body).toEqual({ note: "test" });

    await user.upload(screen.getByLabelText("Backup file"), new File(["x"], "envoryx.tar.gz", { type: "application/gzip" }));
    expect(await screen.findByText(/imported as upload-20260919-090000-cd34/)).toBeInTheDocument();
    const uploadCall = api.calls.find((c) => c.url.endsWith("/upload"))!;
    expect(uploadCall.body).toBeInstanceOf(FormData);
    expect((uploadCall.body as FormData).get("file")).toBeInstanceOf(File);

    await user.click(screen.getByRole("button", { name: "Restore manual-20260918-100000-ab12" }));
    const confirmButton = screen.getByRole("button", { name: "Restore and restart" });
    expect(confirmButton).toBeDisabled();
    await user.type(screen.getByLabelText("Type restore to confirm"), "restore");
    expect(confirmButton).toBeEnabled();
    await user.click(confirmButton);
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/restore"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/restore"))!.body).toEqual({ confirm: "restore" });
    expect(await screen.findByText(/Envoryx is restarting/)).toBeInTheDocument();
  });

  it("shows a scheduled restore and lets the user cancel it", async () => {
    let pending: { id: string; requestedAt: string } | null = { id: "manual-20260918-100000-ab12", requestedAt: "2026-09-19T09:05:00Z" };
    const api = mockApi({
      ...authedRoutes,
      "GET /instance/backups": () => ({ body: { backups: [], pendingRestore: pending, dir: "/backups/_instance", canRestart: false } }),
      "DELETE /instance/restore": () => {
        pending = null;
        return { status: 204 };
      },
    });
    renderApp(<InstanceBackupsCard />);
    const user = userEvent.setup();
    expect(await screen.findByText(/A restore of manual-20260918-100000-ab12 is scheduled/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Cancel restore" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE")).toBe(true));
    await waitFor(() => expect(screen.queryByText(/is scheduled/)).not.toBeInTheDocument());
  });
});
