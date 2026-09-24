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

  it("colours levels and filters the live buffer by level", async () => {
    mockApi({ ...authedRoutes });
    renderApp(<LogsTab project={makeProject()} />);
    const user = userEvent.setup();
    await waitFor(() => expect(FakeSocket.instances).toHaveLength(1));
    const ws = FakeSocket.instances[0]!;
    act(() => {
      ws.onopen?.();
      ws.onmessage?.({ data: JSON.stringify({ type: "line", time: "2026-09-18T10:00:00Z", stream: "stderr", text: "PHP Fatal error: boom", level: "error" }) });
      ws.emit("GET / 200");
    });
    expect(await screen.findByText("PHP Fatal error: boom")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Show"), "error");
    expect(screen.queryByText("GET / 200")).not.toBeInTheDocument();
    expect(screen.getByText("PHP Fatal error: boom").closest("tr")).toHaveClass("text-red-300");
    expect(screen.getByRole("link", { name: "Download" })).toHaveAttribute("href", "/api/v1/projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/services/php/logs/download?");
  });

  it("searches the history with a time range, level and error chart", async () => {
    const base = "/projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/services/php/logs";
    const { calls } = mockApi({
      ...authedRoutes,
      [`GET ${base}/stats`]: () => ({
        body: {
          from: "2026-09-24T09:00:00Z",
          to: "2026-09-24T10:00:00Z",
          bucketSeconds: 3600,
          buckets: [
            { start: "2026-09-24T09:00:00Z", total: 3, warnings: 1, errors: 1 },
            { start: "2026-09-24T10:00:00Z", total: 0, warnings: 0, errors: 0 },
          ],
          total: 3,
          warnings: 1,
          errors: 1,
          top: [{ level: "error", pattern: "PHP Fatal error: Allowed memory size of # bytes", example: "PHP Fatal error: Allowed memory size of 134217728 bytes", count: 4, first: "2026-09-24T09:10:00Z", last: "2026-09-24T09:50:00Z" }],
        },
      }),
      [`GET ${base}`]: () => ({
        body: { lines: [{ time: "2026-09-24T09:10:00Z", stream: "stderr", text: "PHP Fatal error: Allowed memory size of 134217728 bytes", level: "error" }], matched: 5000, truncated: true },
      }),
    });
    renderApp(<LogsTab project={makeProject()} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("radio", { name: "History" }));
    expect(await screen.findByText("PHP Fatal error: Allowed memory size of 134217728 bytes")).toBeInTheDocument();
    expect(screen.getByText("Last 1 of 5000 matching lines – the download has all of them")).toBeInTheDocument();
    expect(calls.some((c) => c.url === `/api/v1${base}?since=24h&tail=2000`)).toBe(true);

    await user.selectOptions(screen.getByLabelText("Time range"), "7d");
    await user.selectOptions(screen.getByLabelText("Level"), "error");
    await waitFor(() => expect(calls.some((c) => c.url === `/api/v1${base}/stats?level=error&since=7d`)).toBe(true));
    expect(screen.getByRole("link", { name: "Download" })).toHaveAttribute("href", `/api/v1${base}/download?level=error&since=7d`);

    // The most frequent problem searches for its text.
    await user.click(screen.getByText("Most frequent problems (1)"));
    await user.click(screen.getByRole("button", { name: "PHP Fatal error: Allowed memory size of # bytes" }));
    await waitFor(() => expect(calls.some((c) => c.url.includes("q=PHP+Fatal+error%3A+Allowed+memory+size+of"))).toBe(true));

    // A bar zooms into its hour.
    const bars = screen.getAllByRole("button", { name: /Errors: 1 · warnings: 1 · lines: 3/ });
    await user.click(bars[bars.length - 1]!);
    await waitFor(() => expect(calls.some((c) => c.url.includes("since=2026-09-24T09%3A00%3A00.000Z&until=2026-09-24T09%3A59%3A59.999Z"))).toBe(true));
    expect(screen.getByLabelText("From")).toBeInTheDocument();
  });
});
