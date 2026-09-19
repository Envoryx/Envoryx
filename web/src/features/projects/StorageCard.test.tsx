import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ServicesTab } from "./ServicesTab";
import { authedRoutes, makeProject, mockApi, renderApp, runtimesFixture } from "@/test/utils";

const P = "/projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const info = {
  version: "1.0", image: "rustfs/rustfs:1.0.0", endpoint: "http://s3:9000", publicUrl: "https://acme-shop-s3.test/acme-shop", hostPort: 20003, consolePort: 20004,
  consolePath: "/rustfs/console/", region: "us-east-1", bucket: "acme-shop", publicRead: true, injectedEnv: ["AWS_BUCKET", "S3_ENDPOINT"], state: "running", health: "healthy",
  volumeName: "envoryx-acme-shop-storage", hostname: "acme-shop-s3.test",
};

describe("StorageCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("offers to add object storage when the project has none", async () => {
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET ${P}/extras`]: () => ({ body: { services: [] } }),
      [`GET ${P}/storage`]: () => ({ status: 404, body: { error: { code: "not_found", message: "no storage" } } }),
    });
    renderApp(<ServicesTab project={makeProject()} />);
    expect(await screen.findByRole("button", { name: "Add object storage" })).toBeInTheDocument();
  });

  it("shows the bucket, reveals keys on request and toggles public read", async () => {
    let publicRead = true;
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "GET /settings": () => ({ body: { publicHost: "192.168.1.10" } }),
      [`GET ${P}/extras`]: () => ({ body: { services: [] } }),
      // Routes match by prefix: the more specific one must come first.
      [`GET ${P}/storage/credentials`]: () => ({ body: { storage: { ...info, publicRead, accessKey: "envoryxabc", secretKey: "verysecretkey" } } }),
      [`GET ${P}/storage`]: () => ({ body: { storage: { ...info, publicRead } } }),
      [`PUT ${P}/storage/public`]: (_url, init) => {
        publicRead = (JSON.parse(init.body as string) as { publicRead: boolean }).publicRead;
        return { body: { storage: { ...info, publicRead } } };
      },
    });
    renderApp(<ServicesTab project={makeProject()} />);
    const user = userEvent.setup();

    expect(await screen.findByText("acme-shop", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByText("http://s3:9000", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open console/ })).toHaveAttribute("href", "http://192.168.1.10:20004/rustfs/console/");
    expect(api.calls.some((c) => c.url.endsWith("/storage/credentials"))).toBe(false);
    // The Laravel snippet carries placeholders until the keys are shown.
    expect(screen.getByText(/AWS_ACCESS_KEY_ID=…/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Show access keys" }));
    expect(await screen.findByText("envoryxabc", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByText(/AWS_ACCESS_KEY_ID=envoryxabc/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Show Secret key" })).toBeInTheDocument();

    const toggle = screen.getByRole("checkbox", { name: /Anyone may read objects/ }) as HTMLInputElement;
    expect(toggle.checked).toBe(true);
    await user.click(toggle);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT" && c.url.endsWith("/storage/public"))).toBe(true));
    await waitFor(() => expect((screen.getByRole("checkbox", { name: /Anyone may read objects/ }) as HTMLInputElement).checked).toBe(false));
  });
});
