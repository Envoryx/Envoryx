import i18n from "@/i18n";
import { describeSchedule, fromCron, toCron, type ScheduleForm } from "./cronSchedule";

describe("cron schedule form", () => {
  it("reads back what it builds", () => {
    for (const expr of ["* * * * *", "*/15 * * * *", "7 * * * *", "30 3 * * *", "0 22 * * 5", "15 4 1 * *"]) {
      const form = fromCron(expr);
      expect(form.kind).not.toBe("custom");
      expect(toCron(form)).toBe(expr);
    }
  });

  it("keeps everything else as a raw expression", () => {
    for (const expr of ["*/7 * * * *", "0 3 * 1 *", "0 3 31 * *", "0 3 * * 1-5", "@daily", "0 */2 * * *", "nonsense"]) {
      const form: ScheduleForm = fromCron(expr);
      expect(form.kind).toBe("custom");
      expect(toCron(form)).toBe(expr);
    }
  });

  it("describes the shapes it knows", () => {
    const t = i18n.t.bind(i18n);
    expect(describeSchedule("*/5 * * * *", t)).toBe("every 5 minutes");
    expect(describeSchedule("5 3 * * *", t)).toBe("daily at 03:05");
    expect(describeSchedule("0 22 * * 5", t)).toBe("every Friday at 22:00");
    expect(describeSchedule("0 3 * * 1-5", t)).toBe("0 3 * * 1-5");
  });
});
