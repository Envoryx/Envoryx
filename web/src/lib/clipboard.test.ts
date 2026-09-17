import { copyText } from "./clipboard";

describe("copyText", () => {
  afterEach(() => vi.restoreAllMocks());

  it("falls back to execCommand when the Clipboard API is unavailable (plain HTTP)", async () => {
    Object.defineProperty(window, "isSecureContext", { value: false, configurable: true });
    const exec = vi.fn(() => true);
    document.execCommand = exec as unknown as typeof document.execCommand;
    expect(await copyText("s3cret")).toBe(true);
    expect(exec).toHaveBeenCalledWith("copy");
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("reports failure instead of throwing", async () => {
    Object.defineProperty(window, "isSecureContext", { value: false, configurable: true });
    document.execCommand = (() => false) as unknown as typeof document.execCommand;
    expect(await copyText("x")).toBe(false);
  });
});
