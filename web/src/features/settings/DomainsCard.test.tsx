import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DomainsCard } from "./DomainsCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const proxy = { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true };
const settings = { publicHost: "", baseDomain: "test", forceHttps: false, proxy };
const tls = { enabled: true, ca: { caSubject: "Staqio Local CA", caFingerprint: "AA:BB", caNotAfter: "2036-01-01T00:00:00Z", custom: null }, proxy, baseDomain: "test", forceHttps: false };

describe("DomainsCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("saves base domain and force-HTTPS, offers the CA download and installs a custom certificate", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings/tls/custom": () => ({ body: tls }),
      "GET /settings/tls": () => ({ body: tls }),
      "GET /settings": () => ({ body: settings }),
      "PATCH /settings": (_u, init) => ({ body: { ...settings, ...JSON.parse(init.body as string) } }),
      "PUT /settings/tls/custom": () => ({ body: { ...tls, ca: { ...tls.ca, custom: { subject: "*.dev.example.com", dnsNames: ["*.dev.example.com"], notAfter: "2027-01-01T00:00:00Z", issuer: "R3", expired: false } } } }),
    });
    renderApp(<DomainsCard />);
    const user = userEvent.setup();

    expect(await screen.findByText("Staqio Local CA")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Download staqio-ca.crt/ })).toHaveAttribute("href", "/api/v1/settings/tls/ca.crt");
    expect(screen.getByText("active")).toBeInTheDocument();

    const base = screen.getByLabelText("Base domain");
    await user.clear(base);
    await user.type(base, "dev.home");
    await user.click(screen.getByRole("checkbox", { name: /Force HTTPS/ }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({ baseDomain: "dev.home", forceHttps: true });

    await user.type(screen.getByLabelText("Certificate (PEM, full chain)"), "CERT");
    await user.type(screen.getByLabelText("Private key (PEM)"), "KEY");
    await user.click(screen.getByRole("button", { name: "Install certificate" }));
    expect(await screen.findByText("Remove custom certificate")).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({ certificate: "CERT", key: "KEY" });
  });

  it("tells macvlan users to point DNS at the container address", async () => {
    const direct = { ...proxy, address: "192.168.1.50" };
    mockApi({
      ...authedRoutes,
      "GET /settings/tls": () => ({ body: { ...tls, proxy: direct } }),
      "GET /settings": () => ({ body: { ...settings, proxy: direct } }),
    });
    renderApp(<DomainsCard />);
    expect(await screen.findByText("Staqio has its own IP address: 192.168.1.50")).toBeInTheDocument();
  });

  it("explains missing port mappings", async () => {
    const unpublished = { ...proxy, httpPort: 0, httpsPort: 0 };
    mockApi({
      ...authedRoutes,
      "GET /settings/tls": () => ({ body: { ...tls, proxy: unpublished } }),
      "GET /settings": () => ({ body: { ...settings, proxy: unpublished } }),
    });
    renderApp(<DomainsCard />);
    expect(await screen.findByText("Map the proxy ports")).toBeInTheDocument();
  });
});
