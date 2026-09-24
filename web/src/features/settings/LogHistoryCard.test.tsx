import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LogHistoryCard } from "./LogHistoryCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const logHistory = {
  enabled: true,
  retentionDays: 7,
  maxMb: 1024,
  available: true,
  dir: "/config/logs",
  usage: { bytes: 5 * 1024 * 1024, files: 12, oldest: "2026-09-18T00:00:00Z" },
  following: 3,
};
const settings = { publicHost: "", baseDomain: "test", forceHttps: false, proxy: { enabled: false }, logHistory };

describe("LogHistoryCard", () => {
  it("shows the usage, saves retention, switches off and deletes the history", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings }),
      "PATCH /settings": (_u, init) => {
        const body = JSON.parse(init.body as string) as { logHistory: object };
        return { body: { ...settings, logHistory: { ...logHistory, ...body.logHistory } } };
      },
      "DELETE /settings/log-history": () => ({ body: { logHistory: { ...logHistory, usage: { bytes: 0, files: 0 } } } }),
    });
    renderApp(<LogHistoryCard />);
    const user = userEvent.setup();

    expect(await screen.findByText("5.0 MB in 12 files", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("Reading 3 containers now", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("on")).toBeInTheDocument();

    const days = screen.getByLabelText("Keep for (days)");
    await user.clear(days);
    await user.type(days, "30");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({ logHistory: { retentionDays: 30, maxMb: 1024 } });
    expect(await screen.findByText("Saved. Retention is applied within the hour.")).toBeInTheDocument();

    await user.click(screen.getByRole("checkbox", { name: /Keep the output of the project containers/ }));
    await waitFor(() => expect(api.calls.filter((c) => c.method === "PATCH")[1]?.body).toEqual({ logHistory: { enabled: false } }));
    await waitFor(() => expect(screen.getByText("off")).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Delete stored logs" }));
    const dialog = screen.getByRole("dialog", { hidden: true });
    await user.click(within(dialog).getByRole("button", { name: "Delete", hidden: true }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE")).toBe(true));
    expect(await screen.findByText("0 B in 0 files", { exact: false })).toBeInTheDocument();
  });
});
