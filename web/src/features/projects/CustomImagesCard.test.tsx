import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CustomImagesCard } from "./CustomImagesCard";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";
import type { Project } from "@/api/types";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";

function withPhp(custom: Project["services"][number]["customImage"], image: string, extra: Partial<Project> = {}): Project {
  const p = makeProject({ id, ...extra });
  return { ...p, services: p.services.map((s) => (s.kind === "php" ? { ...s, image, ...(custom ? { customImage: custom } : {}) } : s)) };
}

describe("CustomImagesCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("sets a registry image and shows what the check found", async () => {
    const ref = "ghcr.io/acme/php:8.4";
    const api = mockApi({
      ...authedRoutes,
      [`PUT /projects/${id}/services/php/image`]: () => ({ body: { project: withPhp({ image: ref, warnings: ["socat is missing: no waiting"], checkedImage: ref }, ref) } }),
    });
    renderApp(<CustomImagesCard project={withPhp(undefined, "ghcr.io/envoryx/envoryx-php:8.4")} />);
    const user = userEvent.setup();

    expect(await screen.findByText("Runtime images")).toBeInTheDocument();
    expect(screen.queryByText("Output of the last build")).not.toBeInTheDocument();
    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();
    await user.selectOptions(screen.getByLabelText("Image"), "image");
    await user.type(screen.getByLabelText("Image reference"), ref);
    await user.click(save);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({ image: ref });
    expect(await screen.findByText(/Saved and applied/)).toBeInTheDocument();
  });

  it("shows warnings, a failed build and rebuilds a Dockerfile", async () => {
    const tag = "envoryx-build/php:0123456789abcdef";
    const api = mockApi({
      ...authedRoutes,
      [`POST /projects/${id}/services/php/image/build`]: () => ({ body: { project: withPhp({ dockerfile: ".envoryx/php.Dockerfile", checkedImage: tag }, tag) } }),
      [`DELETE /projects/${id}/services/php/image`]: () => ({ body: { project: withPhp(undefined, "ghcr.io/envoryx/envoryx-php:8.4") } }),
    });
    const project = withPhp({ dockerfile: ".envoryx/php.Dockerfile", warnings: ["dlv is missing: no debugging"], checkedImage: tag, buildFailed: true, buildOutput: "Step 2/2 : RUN false" }, tag);
    renderApp(<CustomImagesCard project={project} />);
    const user = userEvent.setup();

    expect(await screen.findByText("build failed")).toBeInTheDocument();
    expect(screen.getByText("dlv is missing: no debugging")).toBeInTheDocument();
    expect(screen.getByText("Step 2/2 : RUN false")).toBeVisible();
    expect(screen.getByLabelText("Dockerfile")).toHaveValue(".envoryx/php.Dockerfile");

    await user.click(screen.getByRole("button", { name: "Rebuild without cache" }));
    expect(await screen.findByText("Image rebuilt.")).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Image"), "catalog");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE")).toBe(true));
  });

  it("lets developers rebuild but not change the image, and hides warnings of an older image", async () => {
    mockApi({ ...authedRoutes });
    const project = withPhp({ dockerfile: ".envoryx/php.Dockerfile", warnings: ["old"], checkedImage: "envoryx-build/php:old" }, "envoryx-build/php:new", { access: "operate" });
    renderApp(<CustomImagesCard project={project} />);
    expect(await screen.findByRole("button", { name: "Rebuild without cache" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Image")).not.toBeInTheDocument();
    expect(screen.queryByText("old")).not.toBeInTheDocument();
  });
});
