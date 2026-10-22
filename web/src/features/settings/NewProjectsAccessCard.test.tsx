import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NewProjectsAccessCard } from "./NewProjectsAccessCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

describe("NewProjectsAccessCard", () => {
  it("switches new projects behind the sign-in", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: { publicHost: "", newProjectsPrivate: false } }),
      "PATCH /settings": () => ({ body: {} }),
    });
    renderApp(<NewProjectsAccessCard />);
    const user = userEvent.setup();
    await user.click(await screen.findByLabelText(/New projects only let in people signed in to Envoryx/));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({ newProjectsPrivate: true });
  });

  it("stays on when the plan leaves out open projects", async () => {
    mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: { publicHost: "", newProjectsPrivate: false } }),
      "GET /plan": () => ({ body: { plan: { name: "Team", limits: {}, runtimes: [], disabled: ["publicProjects"], lockedSettings: [] }, usage: { projects: 0, users: 1, diskBytes: 0 } } }),
    });
    renderApp(<NewProjectsAccessCard />);
    expect(await screen.findByText("Your hoster's plan keeps every project behind the sign-in.")).toBeInTheDocument();
    expect(screen.getByLabelText(/New projects only let in people signed in to Envoryx/)).toBeChecked();
    expect(screen.getByLabelText(/New projects only let in people signed in to Envoryx/)).toBeDisabled();
  });
});
