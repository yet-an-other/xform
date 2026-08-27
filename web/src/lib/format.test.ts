import { describe, expect, it } from "vitest";

import { flagEmoji, formatBytes, formatUptime, formatUptimeShort, percentUsed } from "./format";

describe("formatBytes", () => {
  it("renders whole bytes without decimals", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(512)).toBe("512 B");
  });

  it("scales through binary units", () => {
    expect(formatBytes(1536)).toBe("1.50 KiB");
    expect(formatBytes(5_100_273_664)).toBe("4.75 GiB");
  });

  it("drops decimals as the value grows", () => {
    expect(formatBytes(90_194_313_216)).toBe("84.0 GiB");
    expect(formatBytes(171_798_691_840)).toBe("160 GiB");
  });

  it("caps at TiB", () => {
    expect(formatBytes(8 * 1024 ** 4)).toBe("8.00 TiB");
  });
});

describe("formatUptime", () => {
  it("picks the largest readable unit", () => {
    expect(formatUptime(59)).toBe("0 minutes");
    expect(formatUptime(60)).toBe("1 minute");
    expect(formatUptime(7_200)).toBe("2 hours");
    expect(formatUptime(86_400)).toBe("1 day");
    expect(formatUptime(1_987_200)).toBe("23 days");
  });
});

describe("formatUptimeShort", () => {
  it("renders two significant units the way the approved header does", () => {
    expect(formatUptimeShort(529_200)).toBe("6d 3h");
    expect(formatUptimeShort(1_209_600)).toBe("14d 0h");
    expect(formatUptimeShort(18_720)).toBe("5h 12m");
    expect(formatUptimeShort(2_880)).toBe("48m");
    expect(formatUptimeShort(59)).toBe("<1m");
  });
});

describe("flagEmoji", () => {
  it("renders an ISO alpha-2 code as regional-indicator letters", () => {
    expect(flagEmoji("NL")).toBe("🇳🇱");
    expect(flagEmoji("DE")).toBe("🇩🇪");
  });
});

describe("percentUsed", () => {
  it("computes the used share of a total", () => {
    expect(percentUsed(1, 2)).toBe(50);
  });

  it("is zero when there is no total", () => {
    expect(percentUsed(10, 0)).toBe(0);
  });

  it("clamps to the 0–100 range", () => {
    expect(percentUsed(3, 2)).toBe(100);
    expect(percentUsed(-1, 2)).toBe(0);
  });
});
