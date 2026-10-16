import { useState, type FormEvent, type ReactNode } from "react";
import { errorMessage } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Button, Input } from "../components/ui";

function Shell({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="flex min-h-full items-center justify-center p-4">
      <div className="w-full max-w-sm rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="mb-6 flex items-center gap-2">
          <img src="/favicon.svg" alt="" className="h-8 w-8" />
          <div>
            <div className="text-lg font-semibold">Leasorix</div>
            <div className="text-xs text-slate-500 dark:text-slate-400">{title}</div>
          </div>
        </div>
        {children}
      </div>
    </div>
  );
}

export function LoginPage() {
  const { login } = useAuth();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await login(username, password);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Shell title="Anmeldung">
      <form onSubmit={submit} className="space-y-4">
        {error && <Alert>{error}</Alert>}
        <Input label="Benutzername" name="username" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
        <Input label="Passwort" name="password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        <Button type="submit" variant="primary" className="w-full" disabled={busy}>
          Anmelden
        </Button>
      </form>
    </Shell>
  );
}

export function SetupPage() {
  const { setup } = useAuth();
  const [token, setToken] = useState("");
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [password2, setPassword2] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const fe: Record<string, string> = {};
    if (password.length < 10) fe.password = "Mindestens 10 Zeichen";
    if (password !== password2) fe.password2 = "Passwörter stimmen nicht überein";
    setErrors(fe);
    if (Object.keys(fe).length) return;
    setBusy(true);
    setError("");
    try {
      await setup(token.trim(), username, password);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Shell title="Ersteinrichtung">
      <form onSubmit={submit} className="space-y-4">
        <Alert kind="info">
          Lege den ersten Administrator an. Das Setup-Token steht beim ersten Start im Server-Log
          (<code>setup_token</code>).
        </Alert>
        {error && <Alert>{error}</Alert>}
        <Input label="Setup-Token" name="setup_token" value={token} onChange={(e) => setToken(e.target.value)} required autoComplete="off" />
        <Input label="Benutzername" name="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
        <Input label="Passwort" name="password" type="password" autoComplete="new-password" value={password} error={errors.password} onChange={(e) => setPassword(e.target.value)} required />
        <Input label="Passwort wiederholen" name="password2" type="password" autoComplete="new-password" value={password2} error={errors.password2} onChange={(e) => setPassword2(e.target.value)} required />
        <Button type="submit" variant="primary" className="w-full" disabled={busy}>
          Administrator anlegen
        </Button>
      </form>
    </Shell>
  );
}
