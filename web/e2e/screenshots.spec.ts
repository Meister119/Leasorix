import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";

// Generates the README screenshots in docs/images from the demo
// configuration (test/screenshots/demo.yaml) and a few real DHCP clients.
// Run with `make screenshots`; skipped in the normal end-to-end run.
const setupToken = process.env.E2E_SETUP_TOKEN ?? "";
const clientNS = process.env.E2E_CLIENT_NS ?? "";
const out = "../docs/images";

test.skip(!process.env.SCREENSHOTS, "set SCREENSHOTS=1 (make screenshots)");
test.use({ viewport: { width: 1440, height: 900 } });

// Office clients that request a lease over the DHCP link (one macvlan
// interface each, so every client has its own MAC address).
const officeClients = [
  "nb-anna",
  "nb-ben",
  "pc-reception",
  "pc-accounting",
  "ws-cad01",
  "ws-cad02",
  "macbook-eva",
  "iphone-david",
  "tablet-warehouse",
  "pc-meeting1",
  "projector-1f",
  "thinclient-03",
  "nb-frank",
  "nb-greta",
];

function runClients() {
  const lines = officeClients.map((name, i) => {
    const mac = `02:4c:58:00:10:${(i + 1).toString(16).padStart(2, "0")}`;
    return `ip link add m${i} link c0 type macvlan mode bridge && ip link set m${i} address ${mac} up && udhcpc -i m${i} -f -q -n -t 5 -T 1 -s /bin/true -x hostname:${name}`;
  });
  // A network boot client: matched by the PXE boot policy.
  lines.push(
    "ip link add mp link c0 type macvlan mode bridge && ip link set mp address 02:4c:58:00:20:01 up && udhcpc -i mp -f -q -n -t 5 -T 1 -s /bin/true -V PXEClient:Arch:00007:UNDI:003016",
  );
  execFileSync("ip", ["netns", "exec", clientNS, "sh", "-ec", lines.join("\n")], { stdio: ["ignore", "pipe", "pipe"] });
}

// Leases of the relayed networks (no client namespace behind them).
function relayedLeases() {
  const now = Date.now();
  const leases: { ip: string; mac: string; hostname: string; state: string; start: string; expires: string; updated: string }[] = [];
  const add = (ip: string, mac: string, hostname: string, leaseSeconds: number, age: number) => {
    const start = new Date(now - age * 1000);
    leases.push({
      ip,
      mac,
      hostname,
      state: "active",
      start: start.toISOString(),
      expires: new Date(start.getTime() + leaseSeconds * 1000).toISOString(),
      updated: start.toISOString(),
    });
  };
  const guests = ["iPhone", "Galaxy-S24", "Pixel-8", "iPad", "MacBook-Air", "DESKTOP-4K2L9Q", "android-7f3a", ""];
  for (let i = 0; i < 380; i++) {
    const ip = `10.20.${Math.floor((i + 10) / 256)}.${(i + 10) % 256}`;
    add(ip, `0a:20:00:00:${(i >> 8).toString(16).padStart(2, "0")}:${(i & 255).toString(16).padStart(2, "0")}`, guests[i % guests.length], 3600, (i * 37) % 3000);
  }
  for (let i = 0; i < 46; i++) {
    add(`10.30.0.${50 + i}`, `80:5e:c0:00:00:${i.toString(16).padStart(2, "0")}`, `SIP-T54W-${100 + i}`, 691200, (i * 3600) % 500000);
  }
  for (let i = 0; i < 172; i++) {
    add(`192.168.50.${10 + i}`, `0a:50:00:00:00:${i.toString(16).padStart(2, "0")}`, `lab-a-${String(i + 1).padStart(3, "0")}`, 3600, (i * 13) % 3000);
  }
  for (let i = 0; i < 31; i++) {
    add(`192.168.51.${10 + i}`, `0a:51:00:00:00:${i.toString(16).padStart(2, "0")}`, `lab-b-${String(i + 1).padStart(3, "0")}`, 3600, (i * 41) % 3000);
  }
  return { format: "leasorix-leases/v1", exported_at: new Date(now).toISOString(), leases };
}

async function shot(page: Page, name: string) {
  // Let charts and badges settle, then capture the visible window.
  await page.waitForLoadState("networkidle");
  await page.screenshot({ path: `${out}/${name}.png` });
}

test("README screenshots", async ({ page }) => {
  const setup = await page.request.post("/api/v1/setup", { data: { setup_token: setupToken, username: "admin", password: "demo-password-123" } });
  expect(setup.status(), await setup.text()).toBe(201);
  const headers = { "X-CSRF-Token": (await setup.json()).csrf_token as string };

  const imp = await page.request.post("/api/v1/config/import?mode=merge", {
    headers: { ...headers, "Content-Type": "application/yaml" },
    data: readFileSync("../test/screenshots/demo.yaml", "utf8"),
  });
  expect(imp.ok(), await imp.text()).toBeTruthy();
  const leases = await page.request.post("/api/v1/leases/import", { headers, data: relayedLeases() });
  expect(leases.ok(), await leases.text()).toBeTruthy();
  runClients();

  await page.goto("/");
  await expect(page.getByText("Utilization per scope")).toBeVisible();
  await expect(page.getByRole("cell", { name: "Lab A" }).first()).toBeVisible();
  await shot(page, "dashboard");

  await page.getByRole("link", { name: "Scopes" }).click();
  await expect(page.getByRole("link", { name: "Guest Wi-Fi" })).toBeVisible();
  await shot(page, "scopes");

  await page.getByRole("link", { name: "Office LAN" }).click();
  await expect(page.getByText("available", { exact: true })).toBeVisible();
  await shot(page, "scope-detail");
  await page.getByRole("tab", { name: "Policies" }).click();
  await expect(page.getByText("PXE boot").first()).toBeVisible();
  await shot(page, "scope-policies");

  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("link", { name: "Leases" }).click();
  await page.getByLabel("Scope").selectOption({ label: "Office LAN (10.77.0.0/16)" });
  await expect(page.getByRole("cell", { name: "nb-anna" })).toBeVisible();
  await shot(page, "leases");

  await page.getByRole("link", { name: "DNS updates" }).click();
  await page.getByRole("tab", { name: "TSIG keys" }).click();
  await page.getByRole("button", { name: "Add key" }).click();
  await page.getByRole("dialog").getByLabel("Name", { exact: true }).fill("dhcp-corp");
  await page.getByRole("dialog").getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("dialog").getByLabel("BIND (named.conf)")).toBeVisible();
  await shot(page, "dns-key");
  await page.getByRole("dialog").getByRole("button", { name: "Done" }).click();
  await expect(page.getByText("Key saved")).toBeHidden({ timeout: 15_000 });

  await page.getByRole("link", { name: "IPv6 (DHCPv6)" }).click();
  await page.getByRole("link", { name: "Office LAN v6" }).click();
  await expect(page.getByText("Delegable prefixes")).toBeVisible();
  await shot(page, "ipv6");

  await page.getByRole("link", { name: "Dashboard" }).click();
  await expect(page.getByText("Utilization per scope")).toBeVisible();
  await page.getByRole("button", { name: "Switch color scheme" }).click();
  await shot(page, "dashboard-dark");

  // The German UI, light again.
  await page.getByRole("button", { name: "Switch color scheme" }).click();
  await page.getByLabel("Language").selectOption("de");
  await page.getByRole("navigation", { name: "Hauptnavigation" }).getByRole("link", { name: "Scopes" }).click();
  await expect(page.getByRole("button", { name: "Neuer Scope" })).toBeVisible();
  await shot(page, "scopes-de");
});
