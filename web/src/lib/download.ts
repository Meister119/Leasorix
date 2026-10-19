import { messages } from "../i18n";

const m = messages({
  en: { notJson: "{name} is not a valid JSON file" },
  de: { notJson: "{name} ist keine gültige JSON-Datei" },
});

/** Offers content as file download. */
export function downloadFile(name: string, content: string, type = "application/json") {
  const url = URL.createObjectURL(new Blob([content], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** Timestamp for file names, e.g. 20260101-120000. */
export function fileStamp(d = new Date()): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
}

/** Reads and parses a JSON file chosen by the user. */
export async function readJSONFile<T = unknown>(f: File): Promise<T> {
  const text = await f.text();
  try {
    return JSON.parse(text) as T;
  } catch {
    throw new Error(m("notJson", { name: f.name }));
  }
}
