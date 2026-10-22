import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { GuestLinksCard } from "./GuestLinksCard";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const existing = { id: "11111111-2222-4333-8444-555555555555", label: "Acme review", createdBy: "admin", createdAt: "2026-10-23T10:00:00Z", expiresAt: "2026-10-30T10:00:00Z" };

describe("GuestLinksCard", () => {
  it("makes a link, shows its address once and revokes links", async () => {
    let links = [existing];
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/guest-links`]: () => ({ body: { links } }),
      [`POST /projects/${id}/guest-links`]: () => {
        const link = { ...existing, id: "66666666-7777-4888-8999-000000000000", label: "Bob" };
        links = [link, ...links];
        return { status: 201, body: { link: { ...link, url: "https://acme-shop.test/.envoryx/guest?t=envg_abc" } } };
      },
      [`DELETE /projects/${id}/guest-links/`]: () => ({ status: 204 }),
    });
    renderApp(<GuestLinksCard project={makeProject({ id, proxyRules: { login: true } })} />);
    const user = userEvent.setup();

    expect(await screen.findByText("Acme review")).toBeInTheDocument();
    expect(screen.getByText(/not used yet/)).toBeInTheDocument();
    await user.type(screen.getByLabelText("For whom"), "Bob");
    await user.selectOptions(screen.getByLabelText("Valid for"), "30");
    await user.click(screen.getByRole("button", { name: "Create link" }));
    expect(await screen.findByText("https://acme-shop.test/.envoryx/guest?t=envg_abc")).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ label: "Bob", days: 30 });

    await user.click(screen.getAllByRole("button", { name: "Revoke" })[1]!);
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE" && c.url.endsWith(existing.id))).toBe(true));
  });

  it("says when the project is open anyway", async () => {
    mockApi({ ...authedRoutes, [`GET /projects/${id}/guest-links`]: () => ({ body: { links: [] } }) });
    renderApp(<GuestLinksCard project={makeProject({ id })} />);
    expect(await screen.findByText(/The project is open to everyone right now/)).toBeInTheDocument();
  });
});
