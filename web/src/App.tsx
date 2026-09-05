import { AccountPage } from "./features/auth/AccountPage";
import { ActivityPage } from "./features/auth/ActivityPage";
import { AppShell } from "./app/AppShell";
import { Router } from "./app/router";
import { SessionBoundary } from "./features/auth/SessionBoundary";
import { SetupPage } from "./features/auth/SetupPage";
import { UsersPage } from "./features/admin/UsersPage";
import { GeneratorPlaceholder } from "./features/generator/GeneratorPlaceholder";
import { VaultPlaceholder } from "./features/vault/VaultPlaceholder";

function protectedRoute(element: React.ReactNode, requireAdmin?: boolean) {
  // The shell frames the login/rotation views too: the masthead stays while
  // the boundary decides which main content is allowed.
  return (
    <AppShell>
      <SessionBoundary requireAdmin={requireAdmin}>{element}</SessionBoundary>
    </AppShell>
  );
}

// Route table. Protected routes pass through SessionBoundary: no session →
// login; first login → forced password rotation; expiry → back to login.
const routes = [
  { path: "/", element: protectedRoute(<VaultPlaceholder />) },
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
  { path: "/vault", element: protectedRoute(<VaultPlaceholder />) },
  { path: "/vault/:itemId", element: protectedRoute(<VaultPlaceholder />) },
  { path: "/generator", element: protectedRoute(<GeneratorPlaceholder />) },
  { path: "/account", element: protectedRoute(<AccountPage />) },
  { path: "/account/activity", element: protectedRoute(<ActivityPage />) },
  { path: "/admin/users", element: protectedRoute(<UsersPage />, true) },
];

export function App() {
  return <Router routes={routes} />;
}
