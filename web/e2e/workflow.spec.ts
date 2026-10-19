import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";

// Started by test/e2e/run.sh, which provides a fresh server and a client
// network namespace connected to the server's DHCP interface.
const setupToken = process.env.E2E_SETUP_TOKEN ?? "";
const clientNS = process.env.E2E_CLIENT_NS ?? "";
const adminPassword = "e2e-admin-password";

test.describe.configure({ mode: "serial" });

async function login(page: Page, user: string, password: string) {
  await page.goto("/");
  await page.getByLabel("Username").fill(user);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByTestId("current-user")).toHaveText(user);
}

test("initial setup creates the first admin", async ({ page }) => {
  test.skip(!setupToken, "E2E_SETUP_TOKEN not set");
  await page.goto("/");
  await expect(page.getByText("Initial setup")).toBeVisible();
  await page.getByLabel("Setup token").fill("wrong-token");
  await page.getByLabel("Password", { exact: true }).fill(adminPassword);
  await page.getByLabel("Repeat password").fill(adminPassword);
  await page.getByRole("button", { name: "Create administrator" }).click();
  await expect(page.getByRole("alert")).toContainText("invalid setup token");

  await page.getByLabel("Setup token").fill(setupToken);
  await page.getByRole("button", { name: "Create administrator" }).click();
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  await expect(page.getByTestId("current-user")).toHaveText("admin");
});

test("login rejects wrong passwords", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Username").fill("admin");
  await page.getByLabel("Password", { exact: true }).fill("definitely-wrong");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("alert")).toContainText("invalid username or password");
});

test("complete workflow: scope via wizard, real client gets a lease, visible in the UI", async ({ page }) => {
  test.skip(!clientNS, "E2E_CLIENT_NS not set");
  await login(page, "admin", adminPassword);

  // Create a scope with the wizard.
  await page.getByRole("link", { name: "Scopes" }).click();
  await page.getByRole("button", { name: "New scope" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("E2E-LAN");
  await dialog.getByRole("button", { name: "Next" }).click();
  // Frontend validation: host bits set.
  await dialog.getByLabel("Subnet").fill("10.77.0.1/16");
  await dialog.getByLabel("Start address").fill("10.77.0.100");
  await dialog.getByLabel("End address").fill("10.77.0.200");
  await dialog.getByRole("button", { name: "Next" }).click();
  await expect(dialog.getByText(/Host bits set/)).toBeVisible();
  await dialog.getByLabel("Subnet").fill("10.77.0.0/16");
  await dialog.getByLabel("Lease duration").selectOption("3600");
  await dialog.getByRole("button", { name: "Next" }).click();
  await dialog.getByLabel("Router (option 3)").fill("10.77.0.1");
  await dialog.getByLabel("DNS servers (option 6)").fill("10.77.0.53");
  await dialog.getByRole("button", { name: "Next" }).click();
  await dialog.getByRole("button", { name: "Next" }).click();
  await expect(dialog.getByText("10.77.0.100 – 10.77.0.200")).toBeVisible();
  await dialog.getByRole("button", { name: "Create scope" }).click();
  await expect(page.getByRole("heading", { name: "E2E-LAN" })).toBeVisible();

  // Scope options were stored.
  await page.getByRole("tab", { name: "Options" }).click();
  await expect(page.getByRole("cell", { name: "routers" })).toBeVisible();
  await expect(page.getByRole("cell", { name: "10.77.0.53" })).toBeVisible();

  // A real DHCP client in the client namespace gets a lease.
  const out = execFileSync("ip", ["netns", "exec", clientNS, "udhcpc", "-i", "c0", "-f", "-q", "-n", "-t", "5", "-T", "1", "-s", "/bin/true", "-x", "hostname:e2e-client"], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
  expect(out).toBeDefined();

  // The lease is visible in the UI.
  await page.getByRole("link", { name: "Leases" }).click();
  const row = page.getByTestId("lease-10.77.0.100");
  await expect(row).toBeVisible({ timeout: 15_000 });
  await expect(row).toContainText("e2e-client");
  await expect(row).toContainText("active");

  // Convert it into a reservation.
  await row.getByRole("button", { name: "Convert to reservation" }).click();
  await expect(row).toContainText("reserved");
  await page.getByRole("link", { name: "Reservations" }).click();
  await expect(page.getByRole("cell", { name: "e2e-client" })).toBeVisible();

  // Dashboard shows the scope utilisation.
  await page.getByRole("link", { name: "Dashboard" }).click();
  await expect(page.getByRole("link", { name: "E2E-LAN" })).toBeVisible();

  // The audit log records who did it.
  await page.getByRole("link", { name: "Audit log" }).click();
  await page.getByLabel("Action (prefix)").fill("scope.create");
  await page.getByRole("button", { name: "Filter" }).click();
  await expect(page.getByRole("cell", { name: "admin", exact: true })).toBeVisible();
  await page.getByLabel("Action (prefix)").fill("lease.assign");
  await page.getByRole("button", { name: "Filter" }).click();
  await expect(page.getByRole("cell", { name: "dhcp", exact: true })).toBeVisible();
});

test("split scope wizard, HA page and emergency export", async ({ page }) => {
  test.skip(!clientNS, "E2E_CLIENT_NS not set");
  await login(page, "admin", adminPassword);
  await page.getByRole("link", { name: "Scopes" }).click();
  await page.getByRole("link", { name: "E2E-LAN" }).click();

  // Split 80/20: preview, apply, download the partner's scope.
  await page.getByRole("button", { name: "Split scope…" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("10.77.0.100 – 10.77.0.179 (80 addresses)")).toBeVisible();
  await expect(dialog.getByText("10.77.0.180 – 10.77.0.200 (21 addresses)")).toBeVisible();
  await dialog.getByRole("button", { name: "Split" }).click();
  await expect(dialog.getByRole("status")).toContainText("10.77.0.180");
  const [download] = await Promise.all([page.waitForEvent("download"), dialog.getByRole("button", { name: /Download/ }).click()]);
  const bundle = JSON.parse(readFileSync(await download.path(), "utf8"));
  expect(bundle.format).toBe("leasorix-scope/v1");
  expect(bundle.exclusions).toContainEqual({ start: "10.77.0.100", end: "10.77.0.179" });
  await dialog.getByRole("button", { name: "Done" }).click();
  await page.getByRole("tab", { name: "Exclusions" }).click();
  await expect(page.getByRole("cell", { name: "10.77.0.180" })).toBeVisible();

  // HA page without partner: certificate generator and lease export.
  await page.getByRole("link", { name: "High availability" }).click();
  await expect(page.getByText("No partnership configured")).toBeVisible();
  await page.getByRole("button", { name: "Generate" }).click();
  await expect(page.getByRole("button", { name: "dhcp-b-key.pem" })).toBeVisible();
  const [exp] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: "Export" }).click()]);
  const leases = JSON.parse(readFileSync(await exp.path(), "utf8"));
  expect(leases.format).toBe("leasorix-leases/v1");
  expect(leases.leases.length).toBeGreaterThan(0);
});

test("superscopes, configuration export, netsh dry run and backup", async ({ page }) => {
  test.skip(!clientNS, "E2E_CLIENT_NS not set");
  await login(page, "admin", adminPassword);
  await page.getByRole("link", { name: "Scopes" }).click();
  await page.getByLabel("New superscope").fill("Building A");
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("cell", { name: "Building A" })).toBeVisible();
  await page.getByRole("link", { name: "E2E-LAN" }).click();
  // Exact: "New superscope" on the list page must not match while the detail page loads.
  await page.getByLabel("Superscope", { exact: true }).selectOption({ label: "Building A" });
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Scope saved")).toBeVisible();
  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("link", { name: "Scopes" }).click();
  await expect(page.getByRole("row", { name: /E2E-LAN/ }).getByText("Building A")).toBeVisible();
  await expect(page.getByRole("row", { name: /Building A/ }).getByRole("link", { name: "10.77.0.0/16" })).toBeVisible();

  await page.getByRole("link", { name: "Users & settings" }).click();
  await page.getByRole("tab", { name: "Backup & import" }).click();
  const [yaml] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: "YAML" }).click()]);
  const doc = readFileSync(await yaml.path(), "utf8");
  expect(doc).toContain("format: leasorix-config/v1");
  expect(doc).toContain("superscope: Building A");

  await page.getByTestId("netsh-import").setInputFiles("../internal/configio/testdata/netsh_dump.txt");
  await expect(page.getByText(/Dry run successful/)).toBeVisible();
  await expect(page.getByText(/lines not imported/)).toBeVisible();

  const [db] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: "Download backup" }).click()]);
  expect(readFileSync(await db.path()).subarray(0, 15).toString()).toBe("SQLite format 3");
});

test("DNS updates: TSIG key with server snippet, zone, scope settings, registration", async ({ page }) => {
  test.skip(!clientNS, "E2E_CLIENT_NS not set");
  await login(page, "admin", adminPassword);
  await page.getByRole("link", { name: "DNS updates" }).click();
  await page.getByRole("tab", { name: "TSIG keys" }).click();
  await page.getByRole("button", { name: "Add key" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Save" }).click();
  // The generated secret is shown once, ready for BIND and PowerDNS.
  await expect(dialog.getByLabel("BIND (named.conf)")).toHaveValue(/key "leasorix-key" \{\s+algorithm hmac-sha256;\s+secret "[A-Za-z0-9+/]{43}=";/);
  await expect(dialog.getByLabel("PowerDNS (pdnsutil)")).toHaveValue(/pdnsutil import-tsig-key leasorix-key hmac-sha256 /);
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("cell", { name: "leasorix-key" })).toBeVisible();

  await page.getByRole("tab", { name: "Zones" }).click();
  await page.getByRole("button", { name: "Add zone" }).click();
  await dialog.getByLabel("Zone", { exact: true }).fill("e2e.example");
  await dialog.getByLabel("Primary server").fill("192.0.2.53:5399");
  await dialog.getByLabel("TSIG key").selectOption({ label: "leasorix-key (hmac-sha256)" });
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("cell", { name: "e2e.example" })).toBeVisible();

  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("link", { name: "Scopes" }).click();
  await page.getByRole("link", { name: "E2E-LAN" }).click();
  await page.getByRole("tab", { name: "DNS" }).click();
  await page.getByLabel("Register records").selectOption("always");
  await page.getByLabel("Domain").fill("e2e.example");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("DNS settings saved")).toBeVisible();

  // A real client: the registration appears (the zone's server does not
  // exist, so it ends up as an error with a retry).
  execFileSync("ip", ["netns", "exec", clientNS, "udhcpc", "-i", "c0", "-f", "-q", "-n", "-t", "5", "-T", "1", "-s", "/bin/true", "-x", "hostname:e2e-dns"], {
    stdio: ["ignore", "pipe", "pipe"],
  });
  await page.getByRole("link", { name: "DNS updates" }).click();
  await page.getByRole("tab", { name: "Registrations" }).click();
  const row = page.getByRole("row", { name: /e2e-dns\.e2e\.example/ });
  await expect(row).toBeVisible({ timeout: 20_000 });
  await expect(row.getByText(/error|pending/)).toBeVisible();
});

test("IPv6: scope with prefix delegation, option, reservation and export", async ({ page }) => {
  await login(page, "admin", adminPassword);
  await page.getByRole("link", { name: "IPv6 (DHCPv6)" }).click();
  // The E2E server runs without DHCPv6 interfaces; the configuration can still be maintained.
  await expect(page.getByText(/DHCPv6 is not active on this instance/)).toBeVisible();
  await page.getByRole("button", { name: "New IPv6 scope" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name", { exact: true }).fill("E2E-v6");
  await dialog.getByLabel("Prefix (link)").fill("2001:db8:e2e::/64");
  await dialog.getByLabel("Start address").fill("2001:db8:e2e::100");
  await dialog.getByLabel("End address").fill("2001:db8:e2e::1ff");
  await dialog.getByLabel("Delegation pool").fill("2001:db8:e2e::/48");
  await dialog.getByRole("button", { name: "Create scope" }).click();
  // The server rejects a delegation pool that overlaps the link prefix.
  await expect(dialog.getByText("must not overlap the link prefix", { exact: true })).toBeVisible();
  await dialog.getByLabel("Delegation pool").fill("2001:db8:e200::/40");
  await dialog.getByRole("button", { name: "Create scope" }).click();
  await expect(page.getByRole("heading", { name: "E2E-v6" })).toBeVisible();
  await expect(page.getByText("Delegable prefixes").locator("xpath=following-sibling::dd[1]")).toHaveText("65536");

  await page.getByRole("tab", { name: "Options" }).click();
  await page.getByLabel("Option", { exact: true }).selectOption("23");
  await page.getByLabel("Value").fill("2001:db8:e2e::53");
  await page.getByRole("button", { name: "Set" }).click();
  await expect(page.getByRole("cell", { name: "2001:db8:e2e::53" })).toBeVisible();

  await page.getByRole("tab", { name: "Reservations" }).click();
  await page.getByLabel("DUID").fill("00:03:00:01:02:00:00:00:e2:e2");
  await page.getByLabel("IPv6 address").fill("2001:db8:e2f::10");
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("must be an address in 2001:db8:e2e::/64", { exact: true })).toBeVisible();
  await page.getByLabel("IPv6 address").fill("2001:db8:e2e::10");
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("cell", { name: "2001:db8:e2e::10" })).toBeVisible();
  await expect(page.getByRole("cell", { name: "0003000102000000e2e2" })).toBeVisible();

  await page.getByRole("link", { name: "Users & settings" }).click();
  await page.getByRole("tab", { name: "Backup & import" }).click();
  const [yaml] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: "YAML" }).click()]);
  const doc = readFileSync(await yaml.path(), "utf8");
  expect(doc).toContain("scopes6:");
  expect(doc).toContain("prefix: 2001:db8:e2e::/64");
  expect(doc).toContain("duid: 0003000102000000e2e2");
});

test("roles: a read-only user cannot change anything", async ({ page }) => {
  await login(page, "admin", adminPassword);
  await page.getByRole("link", { name: "Users & settings" }).click();
  await page.getByRole("tab", { name: "Users" }).click();
  await page.getByLabel("Username").fill("viewer");
  await page.getByLabel("Password", { exact: true }).fill("viewer-password");
  await page.getByLabel("Role", { exact: true }).selectOption("readonly");
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("cell", { name: "viewer" })).toBeVisible();
  await page.getByRole("button", { name: "Sign out" }).click();

  await login(page, "viewer", "viewer-password");
  await page.getByRole("link", { name: "Scopes" }).click();
  await expect(page.getByRole("heading", { name: "Scopes", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Superscopes" })).toBeVisible();
  await expect(page.getByRole("button", { name: "New scope" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Delete" })).toHaveCount(0);
  await page.getByRole("link", { name: "Users & settings" }).click();
  await expect(page.getByRole("tab", { name: "Users", exact: true })).toHaveCount(0);
});

test("dark mode toggle is remembered", async ({ page }) => {
  await login(page, "admin", adminPassword);
  const html = page.locator("html");
  const wasDark = (await html.getAttribute("class"))?.includes("dark") ?? false;
  await page.getByRole("button", { name: "Switch color scheme" }).click();
  await page.reload();
  await expect(page.getByTestId("current-user")).toBeVisible();
  const isDark = (await html.getAttribute("class"))?.includes("dark") ?? false;
  expect(isDark).toBe(!wasDark);
});

test("language: English by default, German can be selected and is remembered", async ({ page }) => {
  await login(page, "admin", adminPassword);
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(page.getByRole("navigation", { name: "Main navigation" }).getByRole("link", { name: "Users & settings" })).toBeVisible();

  await page.getByLabel("Language").selectOption("de");
  await expect(page.locator("html")).toHaveAttribute("lang", "de");
  const nav = page.getByRole("navigation", { name: "Hauptnavigation" });
  await expect(nav.getByRole("link", { name: "Benutzer & Einstellungen" })).toBeVisible();
  await nav.getByRole("link", { name: "Scopes" }).click();
  await expect(page.getByRole("button", { name: "Neuer Scope" })).toBeVisible();

  // The choice survives a reload and applies to the sign-in page.
  await page.reload();
  await expect(page.getByRole("button", { name: "Neuer Scope" })).toBeVisible();
  await page.getByRole("button", { name: "Abmelden" }).click();
  await expect(page.getByRole("button", { name: "Anmelden" })).toBeVisible();
  await page.getByLabel("Sprache").selectOption("en");
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
});
