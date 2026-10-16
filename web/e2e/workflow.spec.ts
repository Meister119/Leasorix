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
  await page.getByLabel("Benutzername").fill(user);
  await page.getByLabel("Passwort", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Anmelden" }).click();
  await expect(page.getByTestId("current-user")).toHaveText(user);
}

test("initial setup creates the first admin", async ({ page }) => {
  test.skip(!setupToken, "E2E_SETUP_TOKEN not set");
  await page.goto("/");
  await expect(page.getByText("Ersteinrichtung")).toBeVisible();
  await page.getByLabel("Setup-Token").fill("wrong-token");
  await page.getByLabel("Passwort", { exact: true }).fill(adminPassword);
  await page.getByLabel("Passwort wiederholen").fill(adminPassword);
  await page.getByRole("button", { name: "Administrator anlegen" }).click();
  await expect(page.getByRole("alert")).toContainText("invalid setup token");

  await page.getByLabel("Setup-Token").fill(setupToken);
  await page.getByRole("button", { name: "Administrator anlegen" }).click();
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  await expect(page.getByTestId("current-user")).toHaveText("admin");
});

test("login rejects wrong passwords", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Benutzername").fill("admin");
  await page.getByLabel("Passwort", { exact: true }).fill("definitely-wrong");
  await page.getByRole("button", { name: "Anmelden" }).click();
  await expect(page.getByRole("alert")).toContainText("invalid username or password");
});

test("complete workflow: scope via wizard, real client gets a lease, visible in the UI", async ({ page }) => {
  test.skip(!clientNS, "E2E_CLIENT_NS not set");
  await login(page, "admin", adminPassword);

  // Create a scope with the wizard.
  await page.getByRole("link", { name: "Scopes" }).click();
  await page.getByRole("button", { name: "Neuer Scope" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("E2E-LAN");
  await dialog.getByRole("button", { name: "Weiter" }).click();
  // Frontend validation: host bits set.
  await dialog.getByLabel("Subnetz").fill("10.77.0.1/16");
  await dialog.getByLabel("Startadresse").fill("10.77.0.100");
  await dialog.getByLabel("Endadresse").fill("10.77.0.200");
  await dialog.getByRole("button", { name: "Weiter" }).click();
  await expect(dialog.getByText(/Host-Bits gesetzt/)).toBeVisible();
  await dialog.getByLabel("Subnetz").fill("10.77.0.0/16");
  await dialog.getByLabel("Lease-Dauer").selectOption("3600");
  await dialog.getByRole("button", { name: "Weiter" }).click();
  await dialog.getByLabel("Router (Option 3)").fill("10.77.0.1");
  await dialog.getByLabel("DNS-Server (Option 6)").fill("10.77.0.53");
  await dialog.getByRole("button", { name: "Weiter" }).click();
  await dialog.getByRole("button", { name: "Weiter" }).click();
  await expect(dialog.getByText("10.77.0.100 – 10.77.0.200")).toBeVisible();
  await dialog.getByRole("button", { name: "Scope anlegen" }).click();
  await expect(page.getByRole("heading", { name: "E2E-LAN" })).toBeVisible();

  // Scope options were stored.
  await page.getByRole("tab", { name: "Optionen" }).click();
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
  await expect(row).toContainText("aktiv");

  // Convert it into a reservation.
  await row.getByRole("button", { name: "In Reservierung umwandeln" }).click();
  await expect(row).toContainText("reserviert");
  await page.getByRole("link", { name: "Reservierungen" }).click();
  await expect(page.getByRole("cell", { name: "e2e-client" })).toBeVisible();

  // Dashboard shows the scope utilisation.
  await page.getByRole("link", { name: "Dashboard" }).click();
  await expect(page.getByRole("link", { name: "E2E-LAN" })).toBeVisible();

  // The audit log records who did it.
  await page.getByRole("link", { name: "Audit-Log" }).click();
  await page.getByLabel("Aktion (Präfix)").fill("scope.create");
  await page.getByRole("button", { name: "Filtern" }).click();
  await expect(page.getByRole("cell", { name: "admin", exact: true })).toBeVisible();
  await page.getByLabel("Aktion (Präfix)").fill("lease.assign");
  await page.getByRole("button", { name: "Filtern" }).click();
  await expect(page.getByRole("cell", { name: "dhcp", exact: true })).toBeVisible();
});

test("split scope wizard, HA page and emergency export", async ({ page }) => {
  test.skip(!clientNS, "E2E_CLIENT_NS not set");
  await login(page, "admin", adminPassword);
  await page.getByRole("link", { name: "Scopes" }).click();
  await page.getByRole("link", { name: "E2E-LAN" }).click();

  // Split 80/20: preview, apply, download the partner's scope.
  await page.getByRole("button", { name: "Split-Scope…" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("10.77.0.100 – 10.77.0.179 (80 Adressen)")).toBeVisible();
  await expect(dialog.getByText("10.77.0.180 – 10.77.0.200 (21 Adressen)")).toBeVisible();
  await dialog.getByRole("button", { name: "Aufteilen" }).click();
  await expect(dialog.getByRole("status")).toContainText("10.77.0.180");
  const [download] = await Promise.all([page.waitForEvent("download"), dialog.getByRole("button", { name: /herunterladen/ }).click()]);
  const bundle = JSON.parse(readFileSync(await download.path(), "utf8"));
  expect(bundle.format).toBe("leasorix-scope/v1");
  expect(bundle.exclusions).toContainEqual({ start: "10.77.0.100", end: "10.77.0.179" });
  await dialog.getByRole("button", { name: "Fertig" }).click();
  await page.getByRole("tab", { name: "Ausschlüsse" }).click();
  await expect(page.getByRole("cell", { name: "10.77.0.180" })).toBeVisible();

  // HA page without partner: certificate generator and lease export.
  await page.getByRole("link", { name: "Hochverfügbarkeit" }).click();
  await expect(page.getByText("Keine Partnerschaft konfiguriert")).toBeVisible();
  await page.getByRole("button", { name: "Erzeugen" }).click();
  await expect(page.getByRole("button", { name: "dhcp-b-key.pem" })).toBeVisible();
  const [exp] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: "Exportieren" }).click()]);
  const leases = JSON.parse(readFileSync(await exp.path(), "utf8"));
  expect(leases.format).toBe("leasorix-leases/v1");
  expect(leases.leases.length).toBeGreaterThan(0);
});

test("superscopes, configuration export, netsh dry run and backup", async ({ page }) => {
  test.skip(!clientNS, "E2E_CLIENT_NS not set");
  await login(page, "admin", adminPassword);
  await page.getByRole("link", { name: "Scopes" }).click();
  await page.getByLabel("Neuer Superscope").fill("Gebäude A");
  await page.getByRole("button", { name: "Anlegen" }).click();
  await expect(page.getByRole("cell", { name: "Gebäude A" })).toBeVisible();
  await page.getByRole("link", { name: "E2E-LAN" }).click();
  await page.getByLabel("Superscope").selectOption({ label: "Gebäude A" });
  await page.getByRole("button", { name: "Speichern" }).click();
  await expect(page.getByText("Scope gespeichert")).toBeVisible();
  await page.getByRole("navigation", { name: "Hauptnavigation" }).getByRole("link", { name: "Scopes" }).click();
  await expect(page.getByRole("row", { name: /E2E-LAN/ }).getByText("Gebäude A")).toBeVisible();
  await expect(page.getByRole("row", { name: /Gebäude A/ }).getByRole("link", { name: "10.77.0.0/16" })).toBeVisible();

  await page.getByRole("link", { name: "Benutzer & Einstellungen" }).click();
  await page.getByRole("tab", { name: "Sicherung & Import" }).click();
  const [yaml] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: "YAML" }).click()]);
  const doc = readFileSync(await yaml.path(), "utf8");
  expect(doc).toContain("format: leasorix-config/v1");
  expect(doc).toContain("superscope: Gebäude A");

  await page.getByTestId("netsh-import").setInputFiles("../internal/configio/testdata/netsh_dump.txt");
  await expect(page.getByText("Prüfung erfolgreich")).toBeVisible();
  await expect(page.getByText(/Zeilen nicht übernommen/)).toBeVisible();

  const [db] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: "Backup herunterladen" }).click()]);
  expect(readFileSync(await db.path()).subarray(0, 15).toString()).toBe("SQLite format 3");
});

test("DNS updates: TSIG key with server snippet, zone, scope settings, registration", async ({ page }) => {
  test.skip(!clientNS, "E2E_CLIENT_NS not set");
  await login(page, "admin", adminPassword);
  await page.getByRole("link", { name: "DNS-Updates" }).click();
  await page.getByRole("tab", { name: "TSIG-Schlüssel" }).click();
  await page.getByRole("button", { name: "Schlüssel hinzufügen" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Speichern" }).click();
  // The generated secret is shown once, ready for BIND and PowerDNS.
  await expect(dialog.getByLabel("BIND (named.conf)")).toHaveValue(/key "leasorix-key" \{\s+algorithm hmac-sha256;\s+secret "[A-Za-z0-9+/]{43}=";/);
  await expect(dialog.getByLabel("PowerDNS (pdnsutil)")).toHaveValue(/pdnsutil import-tsig-key leasorix-key hmac-sha256 /);
  await dialog.getByRole("button", { name: "Fertig" }).click();
  await expect(page.getByRole("cell", { name: "leasorix-key" })).toBeVisible();

  await page.getByRole("tab", { name: "Zonen" }).click();
  await page.getByRole("button", { name: "Zone hinzufügen" }).click();
  await dialog.getByLabel("Zone", { exact: true }).fill("e2e.example");
  await dialog.getByLabel("Primärer Server").fill("192.0.2.53:5399");
  await dialog.getByLabel("TSIG-Schlüssel").selectOption({ label: "leasorix-key (hmac-sha256)" });
  await dialog.getByRole("button", { name: "Speichern" }).click();
  await expect(page.getByRole("cell", { name: "e2e.example" })).toBeVisible();

  await page.getByRole("navigation", { name: "Hauptnavigation" }).getByRole("link", { name: "Scopes" }).click();
  await page.getByRole("link", { name: "E2E-LAN" }).click();
  await page.getByRole("tab", { name: "DNS" }).click();
  await page.getByLabel("Records registrieren").selectOption("always");
  await page.getByLabel("Domäne").fill("e2e.example");
  await page.getByRole("button", { name: "Speichern" }).click();
  await expect(page.getByText("DNS-Einstellungen gespeichert")).toBeVisible();

  // A real client: the registration appears (the zone's server does not
  // exist, so it ends up as an error with a retry).
  execFileSync("ip", ["netns", "exec", clientNS, "udhcpc", "-i", "c0", "-f", "-q", "-n", "-t", "5", "-T", "1", "-s", "/bin/true", "-x", "hostname:e2e-dns"], {
    stdio: ["ignore", "pipe", "pipe"],
  });
  await page.getByRole("link", { name: "DNS-Updates" }).click();
  await page.getByRole("tab", { name: "Registrierungen" }).click();
  const row = page.getByRole("row", { name: /e2e-dns\.e2e\.example/ });
  await expect(row).toBeVisible({ timeout: 20_000 });
  await expect(row.getByText(/Fehler|ausstehend/)).toBeVisible();
});

test("roles: a read-only user cannot change anything", async ({ page }) => {
  await login(page, "admin", adminPassword);
  await page.getByRole("link", { name: "Benutzer & Einstellungen" }).click();
  await page.getByRole("tab", { name: "Benutzer" }).click();
  await page.getByLabel("Benutzername").fill("viewer");
  await page.getByLabel("Passwort", { exact: true }).fill("viewer-password");
  await page.getByLabel("Rolle", { exact: true }).selectOption("readonly");
  await page.getByRole("button", { name: "Anlegen" }).click();
  await expect(page.getByRole("cell", { name: "viewer" })).toBeVisible();
  await page.getByRole("button", { name: "Abmelden" }).click();

  await login(page, "viewer", "viewer-password");
  await page.getByRole("link", { name: "Scopes" }).click();
  await expect(page.getByRole("heading", { name: "Scopes", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Superscopes" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Neuer Scope" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Löschen" })).toHaveCount(0);
  await page.getByRole("link", { name: "Benutzer & Einstellungen" }).click();
  await expect(page.getByRole("tab", { name: "Benutzer", exact: true })).toHaveCount(0);
});

test("dark mode toggle is remembered", async ({ page }) => {
  await login(page, "admin", adminPassword);
  const html = page.locator("html");
  const wasDark = (await html.getAttribute("class"))?.includes("dark") ?? false;
  await page.getByRole("button", { name: "Farbschema wechseln" }).click();
  await page.reload();
  await expect(page.getByTestId("current-user")).toBeVisible();
  const isDark = (await html.getAttribute("class"))?.includes("dark") ?? false;
  expect(isDark).toBe(!wasDark);
});
