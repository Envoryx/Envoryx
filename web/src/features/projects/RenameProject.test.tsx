import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RenameProjectDialog } from "./ProjectActions";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const withDatabase = makeProject({
  services: [...makeProject().services, { kind: "database", variant: "mariadb", version: "11", image: "mariadb:11", enabled: true, config: {} }],
});

describe("RenameProjectDialog", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("needs the identifier typed out and sends the new name with it", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`POST /projects/${id}/rename`]: () => ({
        body: { project: makeProject({ name: "Acme Blog", slug: "acme-blog", path: "acme-blog" }), renamed: { from: "acme-shop", to: "acme-blog", path: "acme-blog", database: "acme_blog" } },
      }),
    });
    renderApp(<RenameProjectDialog project={withDatabase} open onClose={() => {}} />);
    const user = userEvent.setup();

    const name = await screen.findByLabelText("Project name");
    await user.clear(name);
    await user.type(name, "Acme Blog");
    expect(screen.getByText("Identifier: acme-blog")).toBeInTheDocument();
    // The directory follows the identifier as long as it matched the old one.
    expect((screen.getByLabelText("Directory") as HTMLInputElement).value).toBe("acme-blog");

    const button = () => screen.getByRole("button", { name: "Rename project" });
    expect(button()).toBeDisabled();
    await user.type(screen.getByLabelText("Type acme-shop to confirm"), "acme-shop");
    await waitFor(() => expect(button()).not.toBeDisabled());
    await user.click(button());

    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/rename"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/rename"))!.body).toEqual({
      name: "Acme Blog", confirm: "acme-shop", keepDataNames: false, path: "acme-blog",
    });
  });

  it("offers to keep the data names and refuses a rename that changes nothing", async () => {
    mockApi(authedRoutes);
    renderApp(<RenameProjectDialog project={withDatabase} open onClose={() => {}} />);
    const user = userEvent.setup();

    // The name starts as it is, so there is nothing to rename yet.
    await user.type(await screen.findByLabelText("Type acme-shop to confirm"), "acme-shop");
    expect(screen.getByRole("button", { name: "Rename project" })).toBeDisabled();

    const keep = screen.getByRole("checkbox", { name: /Keep the database and bucket names/ });
    expect(keep).not.toBeChecked();
    await user.click(keep);
    expect(keep).toBeChecked();
  });

  it("leaves a hand-picked directory alone and hides the data option without those services", async () => {
    mockApi(authedRoutes);
    renderApp(<RenameProjectDialog project={makeProject({ path: "customers/acme-shop" })} open onClose={() => {}} />);
    const user = userEvent.setup();

    const name = await screen.findByLabelText("Project name");
    await user.clear(name);
    await user.type(name, "Acme Blog");
    expect((screen.getByLabelText("Directory") as HTMLInputElement).value).toBe("customers/acme-shop");
    expect(screen.queryByRole("checkbox", { name: /Keep the database and bucket names/ })).not.toBeInTheDocument();
  });
});
