import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DuplicateProjectDialog } from "./ProjectActions";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const withDatabase = makeProject({
  services: [...makeProject().services, { kind: "database", variant: "mariadb", version: "11", image: "mariadb:11", enabled: true, config: {} }],
});

describe("DuplicateProjectDialog", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("suggests a name, derives the directory and copies everything by default", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`POST /projects/${id}/duplicate`]: () => ({ status: 201, body: { project: makeProject({ id: "copy-id", name: "Acme Shop Test", slug: "acme-shop-test" }) } }),
    });
    renderApp(<DuplicateProjectDialog project={withDatabase} open onClose={() => {}} />);
    const user = userEvent.setup();

    expect(await screen.findByPlaceholderText("Acme Shop Test")).toBeInTheDocument();
    expect(screen.getByText("Identifier: acme-shop-test")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Duplicate project" }));

    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/duplicate"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/duplicate"))!.body).toEqual({
      name: "Acme Shop Test", files: true, includeDependencies: false, database: true, storage: true, workers: true, git: true, start: false,
    });
  });

  it("sends only the parts that are still checked and refuses the original's name", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`POST /projects/${id}/duplicate`]: () => ({ status: 201, body: { project: makeProject({ id: "copy-id" }) } }),
    });
    renderApp(<DuplicateProjectDialog project={withDatabase} open onClose={() => {}} />);
    const user = userEvent.setup();

    const name = await screen.findByLabelText("Name of the copy");
    await user.type(name, "Acme Shop");
    expect(screen.getByText("The copy needs a name of its own.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Duplicate project" })).toBeDisabled();

    await user.clear(name);
    await user.type(name, "Sandbox");
    await user.click(screen.getByRole("checkbox", { name: new RegExp("Copy the database") }));
    await user.click(screen.getByRole("checkbox", { name: new RegExp("Start the copy when it is ready") }));
    await user.click(screen.getByRole("button", { name: "Duplicate project" }));

    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/duplicate"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/duplicate"))!.body).toMatchObject({ name: "Sandbox", database: false, start: true });
  });

  it("offers the dependency option only with the files and hides parts the project has not", async () => {
    mockApi(authedRoutes);
    renderApp(<DuplicateProjectDialog project={makeProject()} open onClose={() => {}} />);
    const user = userEvent.setup();

    expect(await screen.findByRole("checkbox", { name: new RegExp("Including dependencies") })).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: new RegExp("Copy the database") })).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: new RegExp("Copy the objects of the bucket") })).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: new RegExp("Copy the repository binding") })).not.toBeInTheDocument();

    await user.click(screen.getByRole("checkbox", { name: new RegExp("Copy the project files") }));
    expect(screen.queryByRole("checkbox", { name: new RegExp("Including dependencies") })).not.toBeInTheDocument();
  });
});
