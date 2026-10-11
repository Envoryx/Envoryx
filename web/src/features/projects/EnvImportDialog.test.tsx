import { fireEvent, render, screen } from "@testing-library/react";
import { EnvImportDialog } from "./EnvImportDialog";

describe("EnvImportDialog", () => {
  it("leaves out a value that refers to a variable the file doesn't define", () => {
    render(<EnvImportDialog open onClose={() => {}} current={[]} services={[]} onImport={() => {}} />);
    fireEvent.change(screen.getByLabelText(".env contents"), {
      target: { value: "APP_NAME=Shop\nAPP_URL=${ENVORYX_URL}\nMAIL_FROM=hello@${APP_NAME}.test" },
    });
    // The template's own APP_URL=${ENVORYX_URL} would reach the containers as that text.
    expect(screen.getByLabelText("Import APP_URL")).not.toBeChecked();
    expect(screen.getByText("refers to ENVORYX_URL")).toBeInTheDocument();
    // A reference the file resolves itself is fine.
    expect(screen.getByLabelText("Import MAIL_FROM")).toBeChecked();
    expect(screen.getByLabelText("Import APP_NAME")).toBeChecked();
  });
});
