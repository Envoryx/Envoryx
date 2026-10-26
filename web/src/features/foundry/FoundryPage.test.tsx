import { screen } from "@testing-library/react";
import i18n from "@/i18n";
import { FoundryPage } from "./FoundryPage";
import { mountFilm } from "./film";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

// The film needs a real canvas; here only its labels matter.
vi.mock("./film", () => ({ mountFilm: vi.fn(() => () => {}) }));

describe("FoundryPage", () => {
  it("gives screen readers and tooltips the UI language", async () => {
    mockApi({ ...authedRoutes, "GET /settings": () => ({ body: { version: "0.46.0" } }) });
    await i18n.changeLanguage("de");
    try {
      renderApp(<FoundryPage />);
      expect(await screen.findByRole("button", { name: "Animation neu starten" })).toHaveAttribute("title", "Neu starten (R)");
      expect(screen.getByRole("img", { name: /ASCII-Fabrik in Perspektive/ })).toBeInTheDocument();
      const labels = vi.mocked(mountFilm).mock.calls.at(-1)![1]!;
      expect(labels.play).toBe("Animation abspielen");
      expect(labels.chapter!(2, "Assembly")).toBe("Kapitel 2: Assembly");
    } finally {
      await i18n.changeLanguage("en");
    }
  });
});
