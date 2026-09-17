import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { useAuth } from "./features/auth/AuthContext";
import { LoginPage } from "./features/auth/LoginPage";
import { AppShell } from "./layout/AppShell";
import { DashboardPage } from "./features/dashboard/DashboardPage";
import { ProjectsPage } from "./features/projects/ProjectsPage";
import { NewProjectPage } from "./features/projects/NewProjectPage";
import { ProjectDetailPage } from "./features/projects/ProjectDetailPage";
import { DockerPage } from "./features/docker/DockerPage";
import { SettingsPage } from "./features/settings/SettingsPage";
import { Spinner } from "./components/ui";

function RequireAuth({ children }: { children: React.ReactNode }) {
  const auth = useAuth();
  const location = useLocation();
  if (auth.loading) return <Spinner label="Starting Staqio…" />;
  if (auth.needsSetup) return <Navigate to="/setup" replace />;
  if (!auth.user) return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  return <>{children}</>;
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage mode="login" />} />
      <Route path="/setup" element={<LoginPage mode="setup" />} />
      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route index element={<DashboardPage />} />
        <Route path="/projects" element={<ProjectsPage />} />
        <Route path="/projects/new" element={<NewProjectPage />} />
        <Route path="/projects/:id" element={<ProjectDetailPage />} />
        <Route path="/docker" element={<DockerPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
