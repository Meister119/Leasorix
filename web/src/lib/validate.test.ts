import { describe, expect, it } from "vitest";
import {
  formatIPv4,
  normalizeMAC,
  parseIPv4,
  parsePrefix,
  validateRange,
  validateReservation,
  validateScope,
  validMACPattern,
} from "./validate";

describe("ipv4", () => {
  it("parses and formats", () => {
    expect(parseIPv4("10.0.0.1")).toBe(0x0a000001);
    expect(formatIPv4(0xc0a801ff)).toBe("192.168.1.255");
    for (const bad of ["", "10.0.0", "256.1.1.1", "01.2.3.4", "1.2.3.4.5", "a.b.c.d"]) {
      expect(parseIPv4(bad)).toBeNull();
    }
    expect(parsePrefix("10.0.0.0/24")).toEqual({ network: 0x0a000000, bits: 24 });
    expect(parsePrefix("10.0.0.0")).toBeNull();
    expect(parsePrefix("10.0.0.0/33")).toBeNull();
  });
});

describe("validateScope", () => {
  const ok = { name: "lan", subnet: "192.168.1.0/24", start: "192.168.1.100", end: "192.168.1.200", lease_time: 3600 };
  it("accepts a valid scope", () => {
    expect(validateScope(ok)).toEqual({});
  });
  it("rejects bad input like the backend", () => {
    expect(validateScope({ ...ok, name: " " }).name).toBeDefined();
    expect(validateScope({ ...ok, subnet: "192.168.1.1/24" }).subnet).toMatch(/Host-Bits/);
    expect(validateScope({ ...ok, subnet: "10.0.0.0/31" }).subnet).toMatch(/Präfix/);
    expect(validateScope({ ...ok, start: "192.168.2.1" }).start).toMatch(/Subnetz/);
    expect(validateScope({ ...ok, end: "192.168.1.255" }).end).toMatch(/Broadcast/);
    expect(validateScope({ ...ok, start: "192.168.1.201" }).end).toMatch(/vor dem Anfang/);
    expect(validateScope({ ...ok, lease_time: 5 }).lease_time).toBeDefined();
    expect(validateScope({ ...ok, lease_time: 0 }).lease_time).toBeUndefined();
  });
  it("detects overlaps with other scopes", () => {
    const e = validateScope(ok, [{ name: "big", subnet: "192.168.0.0/16" }]);
    expect(e.subnet).toMatch(/big/);
    expect(validateScope(ok, [{ name: "other", subnet: "192.168.2.0/24" }])).toEqual({});
  });
});

describe("ranges and reservations", () => {
  it("validates ranges within the scope range", () => {
    const within = { start: "10.0.0.100", end: "10.0.0.200" };
    expect(validateRange("10.0.0.110", "10.0.0.120", within)).toEqual({});
    expect(validateRange("10.0.0.10", "10.0.0.120", within).start).toBeDefined();
    expect(validateRange("10.0.0.130", "10.0.0.120").end).toBeDefined();
  });
  it("validates reservations", () => {
    expect(validateReservation({ ip: "10.0.0.5", mac: "AA-BB-CC-DD-EE-FF", client_id: "" }, "10.0.0.0/24")).toEqual({});
    expect(validateReservation({ ip: "10.0.1.5", mac: "aa:bb:cc:dd:ee:ff", client_id: "" }, "10.0.0.0/24").ip).toBeDefined();
    expect(validateReservation({ ip: "10.0.0.5", mac: "", client_id: "" }).mac).toBeDefined();
    expect(validateReservation({ ip: "10.0.0.5", mac: "", client_id: "01:aa:bb" })).toEqual({});
  });
  it("normalises MACs and patterns", () => {
    expect(normalizeMAC("AABB.CCDD.EEFF")).toBe("aa:bb:cc:dd:ee:ff");
    expect(normalizeMAC("aa:bb")).toBeNull();
    expect(validMACPattern("00-15-5D-*")).toBe(true);
    expect(validMACPattern("zz:*")).toBe(false);
  });
});
