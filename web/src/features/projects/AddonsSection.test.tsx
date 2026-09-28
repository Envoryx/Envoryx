import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AddonsSection } from "./AddonsSection";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";
import type { ProjectAddon } from "@/api/types";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";

const keycloak: ProjectAddon = {
  name: "keycloak",
  title: "Keycloak",
  version: "26",
  versions: ["26"],
  image: "quay.io/keycloak/keycloak:26.7",
  host: "keycloak",
  port: 8080,
  hostPort: 20003,
  url: "http://acme-shop-keycloak.envoryx.test",
  injectedEnv: ["KEYCLOAK_URL"],
  credentials: [
    { label: "Admin user", value: "admin" },
    { label: "Admin password", value: "s3cret-pass", secret: true },
  ],
  volumes: ["envoryx-acme-shop-addon-keycloak-data"],
  state: "running",
  installed: true,
};

describe("AddonsSection", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows a project's addon, adds another and removes one with confirmation", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/addons`]: () => ({
        body: { addons: [keycloak], available: [{ name: "elasticsearch", title: "Elasticsearch", versions: ["9", "8"], publishPort: true, hasVolumes: true }] },
      }),
      [`PUT /projects/${id}/addons/`]: () => ({ body: { project: makeProject({ id }) } }),
    });
    renderApp(<AddonsSection project={makeProject({ id })} />);
    const user = userEvent.setup();

    expect(await screen.findByText("Keycloak 26")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /acme-shop-keycloak/ })).toHaveAttribute("href", keycloak.url);
    expect(screen.getByText("KEYCLOAK_URL")).toBeInTheDocument();
    expect(screen.queryByText("s3cret-pass")).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Version"), "8");
    await user.click(screen.getByLabelText("Publish the port on the host"));
    await user.click(screen.getByRole("button", { name: "Add Elasticsearch" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT" && c.url.endsWith("/addons/elasticsearch"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/addons/elasticsearch"))!.body).toEqual({ enabled: true, version: "8", exposePort: true });
    expect(await screen.findByText("Elasticsearch added.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Remove" }));
    const confirm = screen.getAllByRole("button", { name: "Remove" }).at(-1)!;
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText("Confirmation"), "keycloak");
    await user.click(confirm);
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/addons/keycloak"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/addons/keycloak"))!.body).toEqual({ enabled: false, removeData: true });
  });

  it("lets viewers look but not change", async () => {
    mockApi({ ...authedRoutes, [`GET /projects/${id}/addons`]: () => ({ body: { addons: [keycloak], available: [{ name: "soketi", title: "Soketi", versions: ["1.6"] }] } }) });
    renderApp(<AddonsSection project={makeProject({ id, access: "read" })} />);
    expect(await screen.findByText("Keycloak 26")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add Soketi" })).not.toBeInTheDocument();
  });
});
