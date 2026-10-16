import { execFileSync } from "node:child_process";
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
  await expect(page.getByRole("heading", { name: "Scopes" })).toBeVisible();
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
