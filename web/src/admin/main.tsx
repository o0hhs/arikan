import "../styles/app.css";
import "../styles/fonts-mono.css";
// The generated Material themes: the panel has them, the subscription page does not.
import "../styles/themes-m3.css";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { MotionConfig } from "motion/react";
import type { ReactNode } from "react";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ApiError } from "../api/client";
import { Atmosphere } from "../components/atmosphere";
import { getTheme, setTheme } from "../components/theme";
import { ErrorBoundary } from "../components/error-boundary";
import { getPerf, onPerfChange, usePerf } from "../lib/perf";
import { ToastProvider } from "../components/toast";
import { initI18n } from "../i18n";
import { adminDicts } from "../i18n/admin";
import { createAppRouter } from "./router";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      retry: (count, e) => !(e instanceof ApiError && [401, 403, 404].includes(e.status)) && count < 2,
      // Returning to the tab refetches at once — unless the mode quiets this browser down.
      refetchOnWindowFocus: !getPerf(),
    },
  },
});

const router = createAppRouter(queryClient);

// Apply the saved theme before the first render so navigation never resets it.
setTheme(getTheme(), { keep: false });

// The performance mode may switch while the panel runs: the queries opened after follow.
// This document's own switch is heard through the mode's listeners ("storage" fires only
// in the other tabs, where this listener follows it).
const followPerf = () => queryClient.setDefaultOptions({ queries: { refetchOnWindowFocus: !getPerf() } });
onPerfChange(followPerf);
window.addEventListener("storage", (e) => {
  if (e.key === "mikan-perf") followPerf();
});

window.addEventListener("mikan:unauthorized", () => {
  if (router.state.location.pathname === "/login") return;
  queryClient.clear();
  void router.navigate({ to: "/login", search: { next: router.state.location.href } });
});

// Motion follows the system's "reduce motion" like the CSS animations do, and drops its
// own at once while the performance mode is on.
function MotionGate({ children }: { children: ReactNode }) {
  const [perf] = usePerf();
  return <MotionConfig reducedMotion={perf ? "always" : "user"}>{children}</MotionConfig>;
}

// Dictionaries load before the first render: t() stays synchronous everywhere. Pages read
// their texts at render time and subscribe to the language themselves (see page() in
// router.tsx), so a switch redraws them without remounting anything.
void initI18n(adminDicts).then(() => {
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <ErrorBoundary>
        <QueryClientProvider client={queryClient}>
          <MotionGate>
            <ToastProvider>
              <Atmosphere />
              <RouterProvider router={router} />
            </ToastProvider>
          </MotionGate>
        </QueryClientProvider>
      </ErrorBoundary>
    </StrictMode>,
  );
});
