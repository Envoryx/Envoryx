import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ImageUpdatesCard } from "./ImageUpdatesCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const settings = {
  publicHost: "",
  baseDomain: "test",
  forceHttps: false,
  proxy: { enabled: false },
  imageUpdates: { autoRestart: false, hour: 5, check: { checkedAt: "2026-10-22T05:00:00Z", newer: ["ghcr.io/envoryx/envoryx-php:8.5"] } },
};

describe("ImageUpdatesCard", () => {
  it("shows the last check and stores the restart with its hour", async () => {
    let current = settings;
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: current }),
      "PATCH /settings": (_u, init) => {
        const body = JSON.parse(init.body as string) as { imageUpdates: { autoRestart: boolean; hour: number } };
        current = { ...current, imageUpdates: { ...current.imageUpdates, ...body.imageUpdates } };
        return { body: current };
      },
    });
    renderApp(<ImageUpdatesCard />);
    const user = userEvent.setup();

    const box = await screen.findByRole("checkbox", { name: /Restart the running projects/ });
    expect(box).not.toBeChecked();
    expect(screen.getByText(/images with a newer build: 1/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Time")).not.toBeInTheDocument();

    await user.click(box);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({ imageUpdates: { autoRestart: true, hour: 5 } });

    await user.selectOptions(await screen.findByLabelText("Time"), "3");
    await waitFor(() => expect(api.calls.filter((c) => c.method === "PATCH").at(-1)!.body).toEqual({ imageUpdates: { autoRestart: true, hour: 3 } }));
  });
});
