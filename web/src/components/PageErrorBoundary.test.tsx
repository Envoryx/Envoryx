import { render, screen } from "@testing-library/react";
import { PageErrorBoundary } from "./PageErrorBoundary";

function Broken(): never {
  throw new Error("Cannot read properties of null (reading 'length')");
}

describe("PageErrorBoundary", () => {
  it("shows the error instead of a blank page", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <PageErrorBoundary>
        <Broken />
      </PageErrorBoundary>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("This page ran into an error");
    expect(screen.getByText(/reading 'length'/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload the page" })).toBeInTheDocument();
  });
});
