import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UnraidCard } from "./UnraidCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const settings = { publicHost: "", baseDomain: "test", forceHttps: false, folderViewFolder: "", proxy: { enabled: false } };

describe("UnraidCard", () => {
  it("stores the FolderView3 folder", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings }),
      "PATCH /settings": (_u, init) => ({ body: { ...settings, ...JSON.parse(init.body as string) } }),
    });
    renderApp(<UnraidCard />);
    const user = userEvent.setup();

    const input = await screen.findByLabelText("FolderView3 folder");
    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();

    await user.type(input, " Envoryx ");
    await user.click(save);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({ folderViewFolder: "Envoryx" });
    expect(await screen.findByText(/move into the folder the next time they are started/)).toBeInTheDocument();
    expect(screen.getByLabelText("FolderView3 folder")).toHaveValue("Envoryx");
  });
});
