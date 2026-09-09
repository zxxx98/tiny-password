import { AccountPage } from "./features/auth/AccountPage";
import { ActivityPage } from "./features/auth/ActivityPage";
import { AppShell } from "./app/AppShell";
import { Router } from "./app/router";
import { SessionBoundary } from "./features/auth/SessionBoundary";
import { SetupPage } from "./features/auth/SetupPage";
import { TransferPage } from "./features/transfer/TransferPage";
import { UsersPage } from "./features/admin/UsersPage";
import { BackupsPage } from "./features/admin/BackupsPage";
import { AuditPage } from "./features/admin/AuditPage";
import { SettingsPage } from "./features/admin/SettingsPage";
import { GeneratorPage } from "./features/generator/GeneratorPage";
import { VaultPage } from "./features/vault/VaultPage";

function protectedRoute(element: React.ReactNode, requireAdmin?: boolean) {
  // The shell frames the login/rotation views too: the masthead stays while
  // the boundary decides which main content is allowed.
  return (
    <AppShell>
      <SessionBoundary requireAdmin={requireAdmin}>{element}</SessionBoundary>
    </AppShell>
  );
}

const vaultRoute = protectedRoute(<VaultPage />);

// Route table. Protected routes pass through SessionBoundary: no session →
// login; first login → forced password rotation; expiry → back to login.
const routes = [
  { path: "/", element: protectedRoute(<VaultPage />) },
  {
    path: "/setup",
    element: (
      <div className="mx-auto max-w-screen-xl px-4 py-16">
        <div className="max-w-xl">
          <h2 className="font-display text-2xl font-bold">首次初始化</h2>
          <p className="mb-6 mt-2 font-body text-neutral-600">
            创建实例的第一位管理员。此页面只能使用一次。
          </p>
          <SetupPage />
        </div>
      </div>
    ),
  },
  { path: "/vault", element: vaultRoute },
  { path: "/vault/trash", element: protectedRoute(<VaultPage section="trash" />) },
  { path: "/vault/:itemId", element: vaultRoute },
  { path: "/generator", element: protectedRoute(<GeneratorPage />) },
  { path: "/transfer", element: protectedRoute(<TransferPage />) },
  { path: "/account", element: protectedRoute(<AccountPage />) },
  { path: "/account/activity", element: protectedRoute(<ActivityPage />) },
  { path: "/admin/users", element: protectedRoute(<UsersPage />, true) },
  { path: "/admin/backups", element: protectedRoute(<BackupsPage />, true) },
  { path: "/admin/audit", element: protectedRoute(<AuditPage />, true) },
  { path: "/admin/settings", element: protectedRoute(<SettingsPage />, true) },
];

export function App() {
  return <Router routes={routes} />;
}
