import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TwoFactorPolicyCard } from "./TwoFactorPolicyCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const status = (totp: boolean) => () => ({ body: { available: true, totp, passkeys: [], recoveryCodes: 0, required: false } });

describe("TwoFactorPolicyCard", () => {
  it("warns an admin without a second factor before the policy covers them", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: { twoFactorRequired: "off" } }),
      "GET /auth/2fa": status(false),
      "PATCH /settings": () => ({ body: {} }),
    });
    renderApp(<TwoFactorPolicyCard />);
    const user = userEvent.setup();
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/auth/2fa"))).toBe(true));
    await user.selectOptions(await screen.findByLabelText("Required of"), "admins");
    const dialog = await screen.findByRole("dialog", { name: "Require a second factor of yourself too?" });
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(api.calls.some((c) => c.method === "PATCH")).toBe(false);

    await user.selectOptions(screen.getByLabelText("Required of"), "all");
    await user.click(await screen.findByRole("button", { name: "Save and set up now" }));
    await waitFor(() => expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({ twoFactorRequired: "all" }));
  });

  it("saves at once for an admin who has a second factor", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: { twoFactorRequired: "off" } }),
      "GET /auth/2fa": status(true),
      "PATCH /settings": () => ({ body: {} }),
    });
    renderApp(<TwoFactorPolicyCard />);
    const user = userEvent.setup();
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/auth/2fa"))).toBe(true));
    await user.selectOptions(await screen.findByLabelText("Required of"), "admins");
    await waitFor(() => expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({ twoFactorRequired: "admins" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
