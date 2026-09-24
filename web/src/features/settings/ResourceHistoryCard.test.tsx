import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ResourceHistoryCard } from "./ResourceHistoryCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const metrics = { retentionDays: 90, samples: 1200, sizes: 48 };
const settings = { publicHost: "", baseDomain: "test", forceHttps: false, proxy: { enabled: false }, metrics };

describe("ResourceHistoryCard", () => {
  it("changes the retention and deletes the history", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings }),
      "PATCH /settings": (_u, init) => {
        const body = JSON.parse(init.body as string) as { metricsRetentionDays: number };
        return { body: { ...settings, metrics: { ...metrics, retentionDays: body.metricsRetentionDays } } };
      },
      "DELETE /settings/metrics": () => ({ body: { metrics: { ...metrics, samples: 0, sizes: 0 } } }),
    });
    renderApp(<ResourceHistoryCard />);
    const user = userEvent.setup();

    expect(await screen.findByText(/samples and 48 disk space measurements stored/)).toBeInTheDocument();
    const select = screen.getByLabelText("Keep for");
    expect(select).toHaveValue("90");
    await user.selectOptions(select, "365");
    await waitFor(() => expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({ metricsRetentionDays: 365 }));
    expect(await screen.findByText("Saved.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Delete resource history" }));
    const dialog = screen.getByRole("dialog", { hidden: true });
    await user.click(within(dialog).getByRole("button", { name: "Delete", hidden: true }));
    expect(await screen.findByText("0 samples and 0 disk space measurements stored")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete resource history" })).toBeDisabled();
  });
});
