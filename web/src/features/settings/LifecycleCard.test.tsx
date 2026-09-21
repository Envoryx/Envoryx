import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LifecycleCard } from "./LifecycleCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const settings = { publicHost: "", baseDomain: "test", forceHttps: false, projectsFollowEnvoryx: false, proxy: { enabled: false } };

describe("LifecycleCard", () => {
  it("shows the default and stores the opt-in", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings }),
      "PATCH /settings": (_u, init) => ({ body: { ...settings, ...JSON.parse(init.body as string) } }),
    });
    renderApp(<LifecycleCard />);
    const user = userEvent.setup();

    const box = await screen.findByRole("checkbox", { name: /Stop projects with Envoryx/ });
    expect(box).not.toBeChecked();
    expect(screen.getByText("off")).toBeInTheDocument();

    await user.click(box);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({ projectsFollowEnvoryx: true });
    await waitFor(() => expect(screen.getByRole("checkbox", { name: /Stop projects with Envoryx/ })).toBeChecked());
    expect(screen.getByText("on")).toBeInTheDocument();
  });
});
