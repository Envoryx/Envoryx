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
        status = { configured: true, provider: "cloudflare", domain: "dev.example.com", email: "me@example.com", issuing: false, notAfter: "2026-12-17T10:00:00Z", names: ["dev.example.com", "*.dev.example.com"] };
        return { body: { available: true, providers, status } };
      },
      "POST /settings/tls/acme/issue": () => ({ status: 202 }),
    });
    renderApp(<AcmeForm baseDomain="dev.example.com" />);
    const user = userEvent.setup();

    const button = await screen.findByRole("button", { name: "Enable Let's Encrypt" });
    expect(button).toBeDisabled();
    await user.type(screen.getByLabelText("Domain"), "dev.example.com");
    await user.type(screen.getByLabelText("E-mail"), "me@example.com");
    await user.type(screen.getByLabelText("API token"), "cf-secret");
    await user.click(button);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({ provider: "cloudflare", domain: "dev.example.com", email: "me@example.com", credentials: { token: "cf-secret" }, staging: false, useAsBaseDomain: true });

    expect(await screen.findByText("active")).toBeInTheDocument();
    expect(screen.getByText(/renewed automatically/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Renew now" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/acme/issue"))).toBe(true));
  });

  it("shows the last error", async () => {
    mockApi({
      ...authedRoutes,
      "GET /settings/tls/acme": () => ({ body: { available: true, providers, status: { configured: true, provider: "cloudflare", domain: "dev.example.com", email: "x@y.de", issuing: false, lastError: "cloudflare: token rejected" } } }),
    });
    renderApp(<AcmeForm baseDomain="test" />);
    expect(await screen.findByText("cloudflare: token rejected")).toBeInTheDocument();
    expect(screen.getByText("no certificate yet")).toBeInTheDocument();
  });
});

describe("AcmeForm providers", () => {
  afterEach(() => vi.unstubAllGlobals());

  const providerList = [
    { key: "cloudflare", name: "Cloudflare", propagationMinutes: 5, fields: [{ key: "token", label: "API token", secret: true }] },
    {
      key: "netcup",
      name: "netcup",
      propagationMinutes: 20,
      fields: [
        { key: "customerNumber", label: "Customer number", secret: false },
        { key: "apiKey", label: "API key", secret: true },
        { key: "apiPassword", label: "API password", secret: true },
      ],
    },
  ];
  const providers = { cloudflare: "Cloudflare", netcup: "netcup" };

  it("asks for the fields of the chosen provider and keeps stored secrets", async () => {
    const status = { configured: true, provider: "netcup", domain: "dev.example.com", email: "me@example.com", issuing: false, fields: { customerNumber: "12345" }, secrets: ["apiKey", "apiPassword"] };
    const api = mockApi({
      ...authedRoutes,
      "GET /settings/tls/acme": () => ({ body: { available: true, providers, providerList, status } }),
      "PUT /settings/tls/acme": () => ({ body: { available: true, providers, providerList, status } }),
    });
    renderApp(<AcmeForm baseDomain="dev.example.com" />);
    const user = userEvent.setup();

    expect(await screen.findByLabelText("Customer number")).toHaveValue("12345");
    expect(screen.getByLabelText("API key")).toHaveAttribute("placeholder", "••••••••");
    expect(screen.getByText(/waits up to 20 minutes/)).toBeInTheDocument();
    const save = screen.getByRole("button", { name: "Save changes" });
    expect(save).toBeDisabled();
    await user.clear(screen.getByLabelText("Customer number"));
    await user.type(screen.getByLabelText("Customer number"), "54321");
    await user.click(save);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toMatchObject({ provider: "netcup", credentials: { customerNumber: "54321" } });

    // Another provider starts empty and needs its own secret.
    await user.selectOptions(screen.getByLabelText("DNS provider"), "cloudflare");
    expect(screen.queryByLabelText("Customer number")).not.toBeInTheDocument();
    expect(screen.getByLabelText("API token")).toHaveValue("");
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  });
});
