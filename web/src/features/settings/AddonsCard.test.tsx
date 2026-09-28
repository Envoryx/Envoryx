import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AddonsCard } from "./AddonsCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

describe("AddonsCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("lists installed addons, installs examples and URLs and edits a file", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /addons/pgadmin": () => ({ body: { addon: { name: "pgadmin" }, source: "name: pgadmin\n" } }),
      "GET /addons": () => ({
        body: {
          addons: [
            { name: "pgadmin", title: "pgAdmin", file: "pgadmin.yml", versions: [{ version: "9", image: "dpage/pgadmin4:9" }], projects: ["Shop"] },
            { name: "broken", title: "", file: "broken.yml", versions: [], projects: [], error: "addon file: yaml: bad" },
          ],
          examples: [{ name: "soketi", title: "Soketi", description: "WebSockets", versions: [], source: "name: soketi\n" }],
        },
      }),
      "POST /addons": (_u, init) => ({ body: { addon: { name: "x", title: (JSON.parse(init.body as string) as { url?: string }).url ? "From URL" : "Soketi" } } }),
      "DELETE /addons/": () => ({ status: 204 }),
    });
    renderApp(<AddonsCard />);
    const user = userEvent.setup();

    expect(await screen.findByText("pgAdmin")).toBeInTheDocument();
    expect(screen.getByText("Used by Shop")).toBeInTheDocument();
    expect(screen.getByText("addon file: yaml: bad")).toBeInTheDocument();
    // An addon a project uses cannot be removed; an unused one can.
    expect(screen.getByRole("button", { name: "Remove pgadmin" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Remove broken" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE" && c.url.endsWith("/addons/broken"))).toBe(true));

    // The first Install belongs to the URL field, the second to the example.
    await user.click(screen.getAllByRole("button", { name: "Install" })[1]!);
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST")).toBe(true));
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ source: "name: soketi\n" });
    expect(await screen.findByText("Addon Soketi installed.")).toBeInTheDocument();

    await user.type(screen.getByLabelText("Install from a URL"), "https://example.com/a.yml");
    await user.click(screen.getAllByRole("button", { name: "Install" })[0]!);
    await waitFor(() => expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(2));
    expect(api.calls.filter((c) => c.method === "POST")[1]!.body).toEqual({ url: "https://example.com/a.yml" });

    await user.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
    expect(await screen.findByLabelText("Addon file")).toHaveValue("name: pgadmin\n");
  });
});
