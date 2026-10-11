import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SupportAccessCard } from "./SupportAccessCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

describe("SupportAccessCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("allows the hoster's support until revoked and takes it back", async () => {
    let access: { allowed: boolean; until?: string } = { allowed: false };
    const api = mockApi({
      ...authedRoutes,
      "GET /settings/support-access": () => ({ body: { access, managed: true } }),
      "PUT /settings/support-access": (_url, init) => {
        access = JSON.parse(init.body as string) as typeof access;
        return { body: { access, managed: true } };
      },
    });
    renderApp(<SupportAccessCard />);
    const user = userEvent.setup();

    await user.selectOptions(await screen.findByLabelText("For"), "Until revoked");
    await user.click(screen.getByRole("button", { name: "Allow support access" }));
    expect(await screen.findByText("Your hoster's support may sign in until you take the access back.")).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({ allowed: true });

    await user.click(screen.getByRole("button", { name: "Take back" }));
    expect(await screen.findByRole("button", { name: "Allow support access" })).toBeInTheDocument();
  });

  it("shows nothing on an instance no hoster manages", async () => {
    mockApi({ ...authedRoutes, "GET /settings/support-access": () => ({ body: { access: { allowed: false }, managed: false } }) });
    const { container } = renderApp(<SupportAccessCard />);
    await new Promise((r) => setTimeout(r, 50));
    expect(container).toBeEmptyDOMElement();
  });
});
