import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";

export function cx(...c: (string | false | null | undefined)[]) {
  return c.filter(Boolean).join(" ");
}

// ---- buttons -----------------------------------------------------------------

type Variant = "primary" | "secondary" | "danger" | "ghost";

const variants: Record<Variant, string> = {
  primary: "bg-teal-700 text-white hover:bg-teal-800 dark:bg-teal-600 dark:hover:bg-teal-500 shadow-sm",
  secondary:
    "border border-slate-300 bg-white text-slate-800 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100 dark:hover:bg-slate-800",
  danger: "bg-red-600 text-white hover:bg-red-700 dark:bg-red-700 dark:hover:bg-red-600 shadow-sm",
  ghost: "text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
};

export function Button({
  variant = "secondary",
  size = "md",
  className,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: "sm" | "md" }) {
  return (
    <button
      type="button"
      {...props}
      className={cx(
        "inline-flex items-center justify-center gap-1.5 rounded-md font-medium transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-teal-600 disabled:cursor-not-allowed disabled:opacity-50",
        size === "sm" ? "px-2.5 py-1 text-xs" : "px-3.5 py-2 text-sm",
        variants[variant],
        className,
      )}
    />
  );
}

// ---- form fields --------------------------------------------------------------

const inputBase =
  "block w-full rounded-md border bg-white px-3 py-2 text-sm shadow-sm placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-teal-600 dark:bg-slate-900";
const inputOk = "border-slate-300 dark:border-slate-700";
const inputErr = "border-red-500 dark:border-red-500";

export function Field({
  label,
  error,
  hint,
  children,
  id,
}: {
  label: string;
  error?: string;
  hint?: string;
  children: (id: string) => ReactNode;
  id?: string;
}) {
  const gen = useId();
  const fid = id ?? gen;
  return (
    <div className="space-y-1">
      <label htmlFor={fid} className="block text-sm font-medium text-slate-700 dark:text-slate-300">
        {label}
      </label>
      {children(fid)}
      {error ? (
        <p className="text-xs text-red-600 dark:text-red-400" role="alert">
          {error}
        </p>
      ) : hint ? (
        <p className="text-xs text-slate-500 dark:text-slate-400">{hint}</p>
      ) : null}
    </div>
  );
}

export function Input({
  label,
  error,
  hint,
  className,
  ...props
}: InputHTMLAttributes<HTMLInputElement> & { label: string; error?: string; hint?: string }) {
  return (
    <Field label={label} error={error} hint={hint} id={props.id}>
      {(id) => (
        <input id={id} aria-invalid={!!error} {...props} className={cx(inputBase, error ? inputErr : inputOk, className)} />
      )}
    </Field>
  );
}

export function TextArea({
  label,
  error,
  hint,
  className,
  ...props
}: TextareaHTMLAttributes<HTMLTextAreaElement> & { label: string; error?: string; hint?: string }) {
  return (
    <Field label={label} error={error} hint={hint} id={props.id}>
      {(id) => (
        <textarea id={id} aria-invalid={!!error} {...props} className={cx(inputBase, "font-mono", error ? inputErr : inputOk, className)} />
      )}
    </Field>
  );
}

export function Select({
  label,
  error,
  hint,
  className,
  children,
  ...props
}: SelectHTMLAttributes<HTMLSelectElement> & { label: string; error?: string; hint?: string }) {
  return (
    <Field label={label} error={error} hint={hint} id={props.id}>
      {(id) => (
        <select id={id} aria-invalid={!!error} {...props} className={cx(inputBase, error ? inputErr : inputOk, className)}>
          {children}
        </select>
      )}
    </Field>
  );
}

export function Toggle({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean }) {
  return (
    <label className="inline-flex cursor-pointer items-center gap-3 text-sm">
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={cx(
          "relative h-6 w-11 rounded-full transition-colors disabled:opacity-50",
          checked ? "bg-teal-600" : "bg-slate-300 dark:bg-slate-700",
        )}
      >
        <span className={cx("absolute top-0.5 left-0.5 h-5 w-5 rounded-full bg-white shadow transition-transform", checked && "translate-x-5")} />
      </button>
      <span>{label}</span>
    </label>
  );
}

// ---- layout pieces ------------------------------------------------------------

export function Card({ title, actions, children, className }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("rounded-lg border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900", className)}>
      {(title || actions) && (
        <header className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="text-sm font-semibold">{title}</h2>
          <div className="flex flex-wrap gap-2">{actions}</div>
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">{subtitle}</p>}
      </div>
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
    </div>
  );
}

const badgeColors = {
  slate: "bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300",
  green: "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300",
  amber: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300",
  red: "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300",
  blue: "bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300",
  teal: "bg-teal-100 text-teal-800 dark:bg-teal-950 dark:text-teal-300",
};

export function Badge({ color = "slate", children }: { color?: keyof typeof badgeColors; children: ReactNode }) {
  return <span className={cx("inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium", badgeColors[color])}>{children}</span>;
}

export function Table({ head, children, empty }: { head: ReactNode[]; children: ReactNode; empty?: boolean }) {
  return (
    <div className="overflow-x-auto">
      <table className="min-w-full divide-y divide-slate-200 text-sm dark:divide-slate-800">
        <thead>
          <tr>
            {head.map((h, i) => (
              <th key={i} scope="col" className="px-3 py-2 text-left text-xs font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase dark:text-slate-400">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">{children}</tbody>
      </table>
      {empty && <p className="py-8 text-center text-sm text-slate-500 dark:text-slate-400">Keine Einträge vorhanden.</p>}
    </div>
  );
}

export function Td({ children, className, mono }: { children?: ReactNode; className?: string; mono?: boolean }) {
  return <td className={cx("px-3 py-2 align-middle whitespace-nowrap", mono && "font-mono text-xs", className)}>{children}</td>;
}

export function Progress({ percent }: { percent: number }) {
  const p = Math.max(0, Math.min(100, percent));
  const color = p >= 90 ? "bg-red-500" : p >= 75 ? "bg-amber-500" : "bg-teal-600";
  return (
    <div className="flex items-center gap-2" title={`${p.toFixed(1)} %`}>
      <div className="h-2 w-full min-w-16 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-800" role="progressbar" aria-valuenow={Math.round(p)} aria-valuemin={0} aria-valuemax={100}>
        <div className={cx("h-full rounded-full", color)} style={{ width: `${p}%` }} />
      </div>
      <span className="w-12 text-right text-xs tabular-nums text-slate-600 dark:text-slate-400">{p.toFixed(0)} %</span>
    </div>
  );
}

export function Spinner() {
  return (
    <div className="flex justify-center p-8" role="status" aria-label="Lädt">
      <div className="h-6 w-6 animate-spin rounded-full border-2 border-slate-300 border-t-teal-600" />
    </div>
  );
}

export function Alert({ children, kind = "error" }: { children: ReactNode; kind?: "error" | "info" | "success" }) {
  const c = {
    error: "border-red-200 bg-red-50 text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-200",
    info: "border-sky-200 bg-sky-50 text-sky-800 dark:border-sky-900 dark:bg-sky-950 dark:text-sky-200",
    success: "border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-200",
  }[kind];
  return (
    <div className={cx("rounded-md border px-3 py-2 text-sm", c)} role={kind === "error" ? "alert" : "status"}>
      {children}
    </div>
  );
}

export function Tabs<T extends string>({ tabs, value, onChange }: { tabs: { id: T; label: string }[]; value: T; onChange: (v: T) => void }) {
  return (
    <div className="mb-4 flex gap-1 overflow-x-auto border-b border-slate-200 dark:border-slate-800" role="tablist">
      {tabs.map((t) => (
        <button
          key={t.id}
          type="button"
          role="tab"
          aria-selected={value === t.id}
          onClick={() => onChange(t.id)}
          className={cx(
            "-mb-px border-b-2 px-3 py-2 text-sm font-medium whitespace-nowrap",
            value === t.id
              ? "border-teal-600 text-teal-700 dark:text-teal-400"
              : "border-transparent text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-200",
          )}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

// ---- modal --------------------------------------------------------------------

export function Modal({ title, onClose, children, footer, wide }: { title: string; onClose: () => void; children: ReactNode; footer?: ReactNode; wide?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    ref.current?.querySelector<HTMLElement>("input, select, textarea, button")?.focus();
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-slate-950/50 p-4 sm:pt-16" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={ref} role="dialog" aria-modal="true" aria-label={title} className={cx("w-full rounded-lg bg-white shadow-xl dark:bg-slate-900", wide ? "max-w-3xl" : "max-w-lg")}>
        <header className="flex items-center justify-between border-b border-slate-200 px-5 py-3 dark:border-slate-800">
          <h2 className="font-semibold">{title}</h2>
          <button type="button" onClick={onClose} className="rounded p-1 text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-800" aria-label="Schließen">
            ✕
          </button>
        </header>
        <div className="space-y-4 px-5 py-4">{children}</div>
        {footer && <footer className="flex justify-end gap-2 border-t border-slate-200 px-5 py-3 dark:border-slate-800">{footer}</footer>}
      </div>
    </div>
  );
}

// ---- toasts & confirm ----------------------------------------------------------

interface Toast {
  id: number;
  text: string;
  kind: "success" | "error";
}

interface FeedbackCtx {
  toast: (text: string, kind?: "success" | "error") => void;
  confirm: (text: string, opts?: { danger?: boolean; confirmLabel?: string }) => Promise<boolean>;
}

const Feedback = createContext<FeedbackCtx | null>(null);

export function FeedbackProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [pending, setPending] = useState<{ text: string; danger?: boolean; label?: string; resolve: (v: boolean) => void } | null>(null);
  const toast = useCallback((text: string, kind: "success" | "error" = "success") => {
    const id = Date.now() + Math.random();
    setToasts((t) => [...t, { id, text, kind }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), kind === "error" ? 8000 : 4000);
  }, []);
  const confirm = useCallback(
    (text: string, opts?: { danger?: boolean; confirmLabel?: string }) =>
      new Promise<boolean>((resolve) => setPending({ text, danger: opts?.danger, label: opts?.confirmLabel, resolve })),
    [],
  );
  const close = (v: boolean) => {
    pending?.resolve(v);
    setPending(null);
  };
  return (
    <Feedback.Provider value={{ toast, confirm }}>
      {children}
      <div className="fixed right-4 bottom-4 z-[60] flex w-80 max-w-[calc(100vw-2rem)] flex-col gap-2" aria-live="polite">
        {toasts.map((t) => (
          <div
            key={t.id}
            className={cx(
              "rounded-md px-4 py-3 text-sm shadow-lg",
              t.kind === "success" ? "bg-emerald-700 text-white" : "bg-red-700 text-white",
            )}
          >
            {t.text}
          </div>
        ))}
      </div>
      {pending && (
        <Modal
          title="Bitte bestätigen"
          onClose={() => close(false)}
          footer={
            <>
              <Button onClick={() => close(false)}>Abbrechen</Button>
              <Button variant={pending.danger ? "danger" : "primary"} onClick={() => close(true)}>
                {pending.label ?? "Bestätigen"}
              </Button>
            </>
          }
        >
          <p className="text-sm">{pending.text}</p>
        </Modal>
      )}
    </Feedback.Provider>
  );
}

export function useFeedback(): FeedbackCtx {
  const v = useContext(Feedback);
  if (!v) throw new Error("useFeedback outside FeedbackProvider");
  return v;
}
