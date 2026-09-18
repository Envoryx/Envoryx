import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NotificationsCard } from "./NotificationsCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const info = {
  status: { config: { enabled: false, provider: "", kinds: null }, hasToken: false, hasSmtpPassword: false },
  providers: { ntfy: "ntfy", webhook: "Generic webhook (JSON POST)", email: "E-mail (SMTP)" },
  kinds: [
    { kind: "project.unhealthy", description: "A project stopped", default: true },
    { kind: "acme.renewed", description: "Certificate renewed", default: false },
  ],
};

describe("NotificationsCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("configures ntfy with default events and sends a test", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings/notifications": () => ({ body: info }),
      "PUT /settings/notifications": (_u, init) => ({ body: { ...info, status: { ...info.status, config: JSON.parse(init.body as string), hasToken: true } } }),
      "POST /settings/notifications/test": () => ({ status: 204 }),
    });
    renderApp(<NotificationsCard />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("checkbox", { name: /Enable notifications/ }));
    await user.type(screen.getByLabelText("URL"), "https://ntfy.sh/envoryx-x");
    await user.type(screen.getByLabelText(/Access token/), "tk");
    await user.click(screen.getByRole("checkbox", { name: /Certificate renewed/ }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toMatchObject({ enabled: true, provider: "ntfy", url: "https://ntfy.sh/envoryx-x", token: "tk", kinds: ["project.unhealthy", "acme.renewed"] });
    expect(await screen.findByText("Notification settings saved.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Send test" }));
    expect(await screen.findByText("Test notification delivered.")).toBeInTheDocument();
  });

  it("shows e-mail fields for the SMTP provider", async () => {
    mockApi({ ...authedRoutes, "GET /settings/notifications": () => ({ body: info }) });
    renderApp(<NotificationsCard />);
    const user = userEvent.setup();
    await user.selectOptions(await screen.findByLabelText("Channel"), "email");
    expect(screen.getByLabelText("SMTP host")).toBeInTheDocument();
    expect(screen.queryByLabelText("URL")).not.toBeInTheDocument();
  });
});
