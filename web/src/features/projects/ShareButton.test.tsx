import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ShareButton } from "./ShareButton";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";

describe("ShareButton", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shares a running project for the chosen time and ends the share", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/share`]: () => ({ body: { share: { active: false } } }),
      [`POST /projects/${id}/share`]: () => ({ body: { share: { active: true, state: "online", url: "https://brave-little-tunnel.trycloudflare.com", expiresAt: "2026-09-26T12:00:00Z" } } }),
      [`DELETE /projects/${id}/share`]: () => ({ status: 204 }),
    });
    renderApp(<ShareButton project={makeProject({ status: { ...makeProject().status, state: "running" } })} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Share publicly" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/Anyone who has the address/)).toBeInTheDocument();
    await user.selectOptions(within(dialog).getByLabelText("Share for"), "240");
    await user.click(within(dialog).getByRole("button", { name: "Share publicly" }));
    expect(await within(dialog).findByText("https://brave-little-tunnel.trycloudflare.com")).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({ minutes: 240 });
    expect(screen.getAllByRole("button", { name: "Shared" }).length).toBeGreaterThan(0);

    await user.click(within(dialog).getByRole("button", { name: "End the share" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE")).toBe(true));
    expect(await within(dialog).findByLabelText("Share for")).toBeInTheDocument();
  });

  it("cannot share a stopped project", async () => {
    mockApi({ ...authedRoutes, [`GET /projects/${id}/share`]: () => ({ body: { share: { active: false } } }) });
    renderApp(<ShareButton project={makeProject({ status: { ...makeProject().status, state: "stopped" } })} />);
    expect(await screen.findByRole("button", { name: "Share publicly" })).toBeDisabled();
  });
});
