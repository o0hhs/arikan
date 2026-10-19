// The advertising agents' panel: a small page of its own, served under the panel's agent
// path, logged in by the login and password the admin hands out. It talks to the panel
// through /api/agent/..., with the same cookie-and-csrf rules as the admin's own (the
// raw client below keeps both).
import "../styles/app.css";
import "../styles/fonts-mono.css";
// The generated Material themes: the panel has them, the subscription page does not.
import "../styles/themes-m3.css";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ApiError } from "../api/client";
import { ErrorBoundary } from "../components/error-boundary";
import { ToastProvider } from "../components/toast";
import { adminDicts } from "../i18n/admin";
import { initI18n } from "../i18n";
import { App } from "./App";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      // The session's answer is the page's switch, not a retry's business.
      retry: (count, e) => !(e instanceof ApiError && [401, 403].includes(e.status)) && count < 2,
      refetchOnWindowFocus: false,
    },
  },
});

void initI18n(adminDicts).then(() => {
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <ErrorBoundary>
        <QueryClientProvider client={queryClient}>
          <ToastProvider>
            <App />
          </ToastProvider>
        </QueryClientProvider>
      </ErrorBoundary>
    </StrictMode>,
  );
});
