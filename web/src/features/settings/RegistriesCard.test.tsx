import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RegistriesCard } from "./RegistriesCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

describe("RegistriesCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("lists logins without passwords and saves changes", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings/registries": () => ({ body: { registries: [{ host: "ghcr.io", username: "me", hasPassword: true }] } }),
      "PUT /settings/registries": (_u, init) => ({ body: { registries: (JSON.parse(init.body as string) as { registries: { host: string; username: string }[] }).registries.map((r) => ({ ...r, password: undefined, hasPassword: true })) } }),
    });
    renderApp(<RegistriesCard />);
    const user = userEvent.setup();

    expect(await screen.findByDisplayValue("ghcr.io")).toBeInTheDocument();
    expect(screen.getByText("Stored; leave empty to keep it.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Add login" }));
    const hosts = screen.getAllByLabelText("Registry");
    await user.type(hosts[1]!, "registry.example.com:5000");
    await user.type(screen.getAllByLabelText("Username")[1]!, "ci");
    await user.type(screen.getAllByLabelText("Password or token")[1]!, "s3cret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({
      registries: [
        { host: "ghcr.io", username: "me", password: "" },
        { host: "registry.example.com:5000", username: "ci", password: "s3cret" },
      ],
    });
    expect(await screen.findByText("Registry logins saved.")).toBeInTheDocument();
  });
});
