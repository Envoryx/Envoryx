import { screen, waitFor, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LogsTab } from "./LogsTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

class FakeSocket {
  static instances: FakeSocket[] = [];
  url: string;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  constructor(url: string) {
    this.url = url;
    FakeSocket.instances.push(this);
  }
  close() {
    this.closed = true;
  }
  emit(text: string, stream = "stdout") {
    this.onmessage?.({ data: JSON.stringify({ type: "line", time: "2026-09-18T10:00:00Z", stream, text }) });
  }
}

describe("LogsTab", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
    vi.stubGlobal("WebSocket", FakeSocket);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("streams lines, filters, pauses and switches services", async () => {
    mockApi({ ...authedRoutes });
    renderApp(<LogsTab project={makeProject()} />);
    const user = userEvent.setup();
    await waitFor(() => expect(FakeSocket.instances).toHaveLength(1));
    const ws = FakeSocket.instances[0]!;
    expect(ws.url).toContain("/api/v1/projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/services/php/logs/ws");

    act(() => {
      ws.onopen?.();
      ws.emit("fpm is running", "stderr");
      ws.emit("GET /index.php 200");
    });
    expect(await screen.findByText("fpm is running")).toBeInTheDocument();
    expect(screen.getByText("GET /index.php 200")).toBeInTheDocument();
    expect(screen.getByText("live")).toBeInTheDocument();

    await user.type(screen.getByLabelText("Search logs"), "index");
    expect(screen.queryByText("fpm is running")).not.toBeInTheDocument();
    expect(screen.getByText("1 of 2 lines · buffer 5000")).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Search logs"));

    await user.click(screen.getByRole("button", { name: "Pause" }));
    act(() => ws.emit("while paused"));
    await new Promise((r) => setTimeout(r, 80));
    expect(screen.queryByText("while paused")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Resume" }));
    expect(await screen.findByText("while paused")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Caddy 2" }));
    await waitFor(() => expect(FakeSocket.instances).toHaveLength(2));
    expect(ws.closed).toBe(true);
    expect(FakeSocket.instances[1]!.url).toContain("/services/web/logs/ws");
    expect(screen.getByText("No output yet.")).toBeInTheDocument();
  });
});
