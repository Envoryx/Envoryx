import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ShareDialog, SharedBadge } from "./Share";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";

describe("Share", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shares a running project for the chosen time and ends the share", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/share`]: () => ({ body: { share: { active: false } } }),
      [`POST /projects/${id}/share`]: () => ({ body: { share: { active: true, state: "online", url: "https://brave-little-tunnel.trycloudflare.com", expiresAt: "2026-09-26T12:00:00Z" } } }),
      [`DELETE /projects/${id}/share`]: () => ({ status: 204 }),
    });
    const project = makeProject({ status: { ...makeProject().status, state: "running" } });
    renderApp(
      <>
        <SharedBadge project={project} onOpen={() => {}} />
        <ShareDialog project={project} open onClose={() => {}} />
      </>,
    );
    const user = userEvent.setup();
    const dialog = await screen.findByRole("dialog");
    expect(screen.queryByRole("button", { name: "Shared" })).not.toBeInTheDocument();
    expect(within(dialog).getByText(/Anyone who has the address/)).toBeInTheDocument();
    await user.selectOptions(within(dialog).getByLabelText("Share for"), "240");
    await user.click(within(dialog).getByRole("button", { name: "Share publicly" }));
    expect(await within(dialog).findByText("https://brave-little-tunnel.trycloudflare.com")).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({ minutes: 240 });
    expect(await screen.findByRole("button", { name: "Shared" })).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "End the share" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE")).toBe(true));
    expect(await within(dialog).findByLabelText("Share for")).toBeInTheDocument();
  });

  it("cannot share a stopped project", async () => {
    mockApi({ ...authedRoutes, [`GET /projects/${id}/share`]: () => ({ body: { share: { active: false } } }) });
    renderApp(<ShareDialog project={makeProject({ status: { ...makeProject().status, state: "stopped" } })} open onClose={() => {}} />);
    expect(await within(await screen.findByRole("dialog")).findByRole("button", { name: "Share publicly" })).toBeDisabled();
  });
});
