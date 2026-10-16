import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Navigate, Route, Routes } from "react-router";
import "./index.css";
import { AuthProvider, useAuth } from "./auth";
import { Layout } from "./components/Layout";
import { FeedbackProvider, Spinner } from "./components/ui";
import { LoginPage, SetupPage } from "./pages/Login";
import { DashboardPage } from "./pages/Dashboard";
import { ScopesPage } from "./pages/Scopes";
import { ScopeDetailPage } from "./pages/ScopeDetail";
import { ReservationsPage } from "./pages/Reservations";
import { LeasesPage } from "./pages/Leases";
import { PoliciesPage } from "./pages/Policies";
import { HAPage } from "./pages/HA";
import { DNSPage } from "./pages/DNS";
import { AuditPage } from "./pages/Audit";
import { SettingsPage } from "./pages/Settings";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false, staleTime: 5_000 } },
});

function Gate() {
  const { status } = useAuth();
  if (status === "loading") return <Spinner />;
  if (status === "setup") return <SetupPage />;
  if (status === "anonymous") return <LoginPage />;
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<DashboardPage />} />
        <Route path="scopes" element={<ScopesPage />} />
        <Route path="scopes/:id" element={<ScopeDetailPage />} />
        <Route path="reservations" element={<ReservationsPage />} />
        <Route path="leases" element={<LeasesPage />} />
        <Route path="policies" element={<PoliciesPage />} />
        <Route path="ha" element={<HAPage />} />
        <Route path="dns" element={<DNSPage />} />
        <Route path="audit" element={<AuditPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <FeedbackProvider>
          <AuthProvider>
            <Gate />
          </AuthProvider>
        </FeedbackProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
