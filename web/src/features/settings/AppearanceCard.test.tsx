import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AppearanceCard } from "./AppearanceCard";
import { renderApp } from "@/test/utils";

describe("AppearanceCard", () => {
  afterEach(() => {
    localStorage.clear();
    delete document.documentElement.dataset.accent;
  });

  it("switches the accent on the document and remembers it; mint clears both", async () => {
    renderApp(<AppearanceCard />);
    const user = userEvent.setup();
    expect(screen.getByRole("radio", { name: "Mint" })).toHaveAttribute("aria-checked", "true");

    await user.click(screen.getByRole("radio", { name: "Ocean" }));
    expect(document.documentElement.dataset.accent).toBe("ocean");
    expect(localStorage.getItem("envoryx.accent")).toBe("ocean");
    expect(screen.getByRole("radio", { name: "Ocean" })).toHaveAttribute("aria-checked", "true");

    await user.click(screen.getByRole("radio", { name: "Mint" }));
    expect(document.documentElement.dataset.accent).toBeUndefined();
    expect(localStorage.getItem("envoryx.accent")).toBeNull();
  });

  it("starts from the stored accent", () => {
    localStorage.setItem("envoryx.accent", "violet");
    renderApp(<AppearanceCard />);
    expect(screen.getByRole("radio", { name: "Violet" })).toHaveAttribute("aria-checked", "true");
    expect(document.documentElement.dataset.accent).toBe("violet");
  });
});
