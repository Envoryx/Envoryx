import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AcmeForm } from "./AcmeForm";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const providers = { cloudflare: "Cloudflare" };

describe("AcmeForm", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("enables Let's Encrypt and shows the certificate status", async () => {
    let status: Record<string, unknown> = { configured: false, issuing: false };
    const api = mockApi({
      ...authedRoutes,
      "GET /settings/tls/acme": () => ({ body: { available: true, providers, status } }),
      "PUT /settings/tls/acme": () => {
        status = { configured: true, provider: "cloudflare", domain: "dev.koze25.de", email: "me@koze25.de", issuing: false, notAfter: "2026-12-17T10:00:00Z", names: ["dev.koze25.de", "*.dev.koze25.de"] };
        return { body: { available: true, providers, status } };
      },
      "POST /settings/tls/acme/issue": () => ({ status: 202 }),
    });
    renderApp(<AcmeForm baseDomain="dev.koze25.de" />);
    const user = userEvent.setup();

    const button = await screen.findByRole("button", { name: "Enable Let's Encrypt" });
    expect(button).toBeDisabled();
    await user.type(screen.getByLabelText("Domain"), "dev.koze25.de");
    await user.type(screen.getByLabelText("E-mail"), "me@koze25.de");
    await user.type(screen.getByLabelText("API token"), "cf-secret");
    await user.click(button);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({ provider: "cloudflare", domain: "dev.koze25.de", email: "me@koze25.de", token: "cf-secret", staging: false, useAsBaseDomain: true });

    expect(await screen.findByText("active")).toBeInTheDocument();
    expect(screen.getByText(/renewed automatically/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Renew now" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/acme/issue"))).toBe(true));
  });

  it("shows the last error", async () => {
    mockApi({
      ...authedRoutes,
      "GET /settings/tls/acme": () => ({ body: { available: true, providers, status: { configured: true, provider: "cloudflare", domain: "dev.koze25.de", email: "x@y.de", issuing: false, lastError: "cloudflare: token rejected" } } }),
    });
    renderApp(<AcmeForm baseDomain="test" />);
    expect(await screen.findByText("cloudflare: token rejected")).toBeInTheDocument();
    expect(screen.getByText("no certificate yet")).toBeInTheDocument();
  });
});
