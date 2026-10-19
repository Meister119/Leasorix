import { createContext, Fragment, useContext, useEffect, useState, type ReactNode } from "react";

/**
 * Minimal translation layer. English is the default; German can be chosen
 * in the UI and is remembered per browser.
 *
 * Texts live next to the code that uses them:
 *
 *   const m = messages({
 *     en: { saved: "Scope “{name}” saved" },
 *     de: { saved: "Scope „{name}“ gespeichert" },
 *   });
 *   toast(m("saved", { name }));
 *
 * The German table must have exactly the keys of the English one (checked
 * by the compiler). The whole app is remounted when the language changes,
 * so m() can be called anywhere during rendering, also outside components.
 */

export type Lang = "en" | "de";

export const languages: { id: Lang; label: string }[] = [
  { id: "en", label: "English" },
  { id: "de", label: "Deutsch" },
];

const storageKey = "leasorix-lang";

function stored(): Lang {
  try {
    const v = localStorage.getItem(storageKey);
    if (v === "en" || v === "de") return v;
  } catch {
    /* storage unavailable */
  }
  return "en";
}

let current: Lang = stored();

/** The active language. */
export function lang(): Lang {
  return current;
}

/** The BCP 47 locale for dates and numbers. */
export function locale(): string {
  return current === "de" ? "de-DE" : "en-US";
}

export type Vars = Record<string, string | number>;

function fill(s: string, vars?: Vars): string {
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (all, k: string) => (k in vars ? String(vars[k]) : all));
}

/** Declares the texts of a module and returns the lookup function. */
export function messages<const E extends Record<string, string>>(m: { en: E; de: { [K in keyof E]: string } }) {
  return (key: keyof E & string, vars?: Vars): string => fill((current === "de" ? m.de[key] : undefined) ?? m.en[key], vars);
}

interface LanguageCtx {
  lang: Lang;
  setLang: (l: Lang) => void;
}

const Ctx = createContext<LanguageCtx>({ lang: current, setLang: () => {} });

export function LanguageProvider({ children }: { children: ReactNode }) {
  const [l, setL] = useState<Lang>(current);
  useEffect(() => {
    document.documentElement.lang = l;
  }, [l]);
  const setLang = (next: Lang) => {
    current = next;
    try {
      localStorage.setItem(storageKey, next);
    } catch {
      /* storage unavailable */
    }
    setL(next);
  };
  // Remount everything below on a change, so every text is rendered again.
  return (
    <Ctx.Provider value={{ lang: l, setLang }}>
      <Fragment key={l}>{children}</Fragment>
    </Ctx.Provider>
  );
}

export function useLanguage(): LanguageCtx {
  return useContext(Ctx);
}
