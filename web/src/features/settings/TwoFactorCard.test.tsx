import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TwoFactorCard } from "./TwoFactorCard";
import { mockApi, renderApp } from "@/test/utils";

const me = {
  "GET /setup": () => ({ body: { needsSetup: false } }),
  "GET /auth/me": () => ({ body: { user: { id: "u1", username: "alice", role: "developer" } } }),
};

describe("TwoFactorCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("sets up the authenticator app after the password and shows the recovery codes once", async () => {
    let totp = false;
    const onChanged = vi.fn();
    const api = mockApi({
      ...me,
      "GET /auth/2fa": () => ({ body: { available: true, totp, passkeys: [], recoveryCodes: totp ? 10 : 0, required: false } }),
      "POST /auth/2fa/totp/setup": (_url, init) => {
        if (JSON.parse(init.body as string).password !== "alice-secret-123") return { status: 403, body: { error: { code: "wrong_password", message: "the current password is wrong" } } };
        return { body: { secret: "JBSWY3DPEHPK3PXP", uri: "otpauth://totp/Envoryx:alice?secret=JBSWY3DPEHPK3PXP", qr: "<svg></svg>" } };
      },
      "POST /auth/2fa/totp": () => {
        totp = true;
        return { body: { recoveryCodes: ["aaaaa-bbbbb", "ccccc-ddddd"] } };
      },
    });
    renderApp(<TwoFactorCard onChanged={onChanged} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Set up" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("Password"), "wrong");
    await user.click(within(dialog).getByRole("button", { name: "Continue" }));
    expect(await within(dialog).findByText("The password is wrong.")).toBeInTheDocument();
    await user.clear(within(dialog).getByLabelText("Password"));
    await user.type(within(dialog).getByLabelText("Password"), "alice-secret-123");
    await user.click(within(dialog).getByRole("button", { name: "Continue" }));

    expect(await screen.findByText("JBSWY3DPEHPK3PXP")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Code"), "123456");
    await user.click(screen.getByRole("button", { name: "Verify" }));
    expect(await screen.findByText("aaaaa-bbbbb")).toBeInTheDocument();
    // The setup page leaves only once the codes are put away.
    expect(onChanged).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "I saved them" }));
    expect(onChanged).toHaveBeenCalledTimes(1);
    expect(api.calls.find((c) => c.url.endsWith("/auth/2fa/totp") && c.method === "POST")?.body).toEqual({ code: "123456" });
  });

  it("points a single sign-on account to its identity provider", async () => {
    mockApi({ ...me, "GET /auth/2fa": () => ({ body: { available: false, totp: false, passkeys: [], recoveryCodes: 0, required: false } }) });
    renderApp(<TwoFactorCard />);
    expect(await screen.findByText("Your account signs in through single sign-on. Set up two-factor authentication with your identity provider.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Set up" })).not.toBeInTheDocument();
  });
});
