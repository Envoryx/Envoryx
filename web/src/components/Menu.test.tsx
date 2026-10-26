import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MoreHorizontal } from "lucide-react";
import { Menu } from "./ui";

describe("Menu", () => {
  it("leaves the focus on its button when an entry is picked, for the dialog it opens to return to", async () => {
    let focused: Element | null = null;
    render(<Menu label="More actions" icon={<MoreHorizontal />} items={[{ label: "Duplicate", onSelect: () => (focused = document.activeElement) }]} />);
    const user = userEvent.setup();
    const button = screen.getByRole("button", { name: "More actions" });
    await user.click(button);
    await user.click(screen.getByRole("menuitem", { name: "Duplicate" }));
    expect(focused).toBe(button);
    expect(document.activeElement).toBe(button);
  });
});
