import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TestsTab } from "./TestsTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

class FakeSocket {
  static instances: FakeSocket[] = [];
  url: string;
  readyState = 1;
  binaryType = "blob";
  sent: string[] = [];
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(url: string) {
    this.url = url;
    FakeSocket.instances.push(this);
  }
  send(d: string) {
    this.sent.push(d);
  }
  close() {
    this.readyState = 3;
  }
}

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const suites = [
  { id: "phpunit", framework: "phpunit", label: "PHPUnit", service: "php", cmd: ["vendor/bin/phpunit"], report: true, filterHint: "--filter", available: true },
  { id: "npm:test", framework: "npm", label: "npm test", service: "node", cmd: ["npm", "test"], report: false, available: false, reason: "node container is not running" },
];
const failedRun = {
  id: "11111111-2222-4333-8444-555555555555",
  suite: "phpunit",
  filter: "CartTest",
  status: "failed",
  exitCode: 1,
  startedAt: "2026-09-25T20:00:00Z",
  durationMs: 2300,
  result: {
    report: true, tests: 3, failures: 1, errors: 0, skipped: 0, seconds: 0.2,
    failed: [{ name: "test_total", class: "Tests.Unit.CartTest", file: "tests/Unit/CartTest.php", line: 20, kind: "failure", message: "Failed asserting that 41 matches expected 42.", details: "diff here" }],
  },
};

describe("TestsTab", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
    vi.stubGlobal("WebSocket", FakeSocket);
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  });
  afterEach(() => vi.unstubAllGlobals());

  it("runs a suite with a filter and shows the failed tests of the result", async () => {
    mockApi({ ...authedRoutes, [`GET /projects/${id}/tests`]: () => ({ body: { suites, runs: [] } }) });
    renderApp(<TestsTab project={makeProject()} />);
    const user = userEvent.setup();
    expect(await screen.findByText("PHPUnit")).toBeInTheDocument();
    expect(screen.getByText("node container is not running")).toBeInTheDocument();
    const buttons = screen.getAllByRole("button", { name: "Run" });
    expect(buttons[1]).toBeDisabled();

    await user.type(screen.getByLabelText("Filter for PHPUnit"), "CartTest");
    await user.click(buttons[0]!);
    await waitFor(() => expect(FakeSocket.instances).toHaveLength(1));
    expect(FakeSocket.instances[0]!.url).toContain(`/projects/${id}/tests/phpunit/ws?filter=CartTest&cols=`);

    act(() => FakeSocket.instances[0]!.onmessage?.({ data: JSON.stringify({ type: "exit", code: 1 }) }));
    act(() => FakeSocket.instances[0]!.onmessage?.({ data: JSON.stringify({ type: "result", run: failedRun }) }));
    expect(await screen.findByText("1 of 3 failed")).toBeInTheDocument();
    expect(screen.getByText("Failed asserting that 41 matches expected 42.")).toBeInTheDocument();
    // A single failure is opened right away.
    expect(screen.getByText("diff here")).toBeInTheDocument();
    expect(screen.getByText(/tests\/Unit\/CartTest.php:20/)).toBeInTheDocument();
  });

  it("lists recent runs and opens one", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/tests`]: () => ({ body: { suites, runs: [{ ...failedRun, result: { ...failedRun.result, output: "" } }] } }),
      [`GET /projects/${id}/test-runs/${failedRun.id}`]: () => ({ body: { run: failedRun } }),
    });
    renderApp(<TestsTab project={makeProject()} />);
    const user = userEvent.setup();
    await user.click(await screen.findByText("· CartTest"));
    expect(await screen.findByText("Failed asserting that 41 matches expected 42.")).toBeInTheDocument();
    expect(api.calls.some((c) => c.url.endsWith(`/test-runs/${failedRun.id}`))).toBe(true);
  });
});
