import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ActivityNotice } from "./ActivityNotice";
import { renderApp } from "@/test/utils";

const activity = [
  { at: "2026-09-21T06:00:10Z", kind: "docker.orphans_removed", items: ["container envoryx-ghost-php", "network envoryx-ghost"] },
  { at: "2026-09-21T06:00:00Z", kind: "projects.resumed", items: ["Blog", "Shop"] },
];

describe("ActivityNotice", () => {
  afterEach(() => localStorage.clear());

  it("tells what Envoryx did on its own and stays dismissed for the same entries", async () => {
    const { unmount } = renderApp(<ActivityNotice activity={activity} />);
    expect(screen.getByText("Envoryx acted on its own")).toBeInTheDocument();
    expect(screen.getByText(/Started the projects again that were running before the restart: Blog, Shop/)).toBeInTheDocument();
    expect(screen.getByText(/Removed orphaned resources that belonged to no project: container envoryx-ghost-php, network envoryx-ghost/)).toBeInTheDocument();

    await userEvent.setup().click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText("Envoryx acted on its own")).not.toBeInTheDocument();
    unmount();

    renderApp(<ActivityNotice activity={activity} />);
    expect(screen.queryByText("Envoryx acted on its own")).not.toBeInTheDocument();

    // A newer action shows the notice again.
    unmount();
    renderApp(<ActivityNotice activity={[{ at: "2026-09-21T07:00:00Z", kind: "projects.resumed", items: ["Docs"] }, ...activity]} />);
    expect(screen.getByText("Envoryx acted on its own")).toBeInTheDocument();
  });

  it("renders nothing without activity", () => {
    const { container } = renderApp(<ActivityNotice activity={[]} />);
    expect(container.textContent).toBe("");
  });
});
