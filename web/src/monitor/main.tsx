import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "../styles/app.css";
import "../styles/fonts-mono.css";
import { ErrorBoundary } from "../components/error-boundary";
import { M3eRoot } from "../m3e/root";
import { initI18n } from "../i18n";
import { monitorDicts } from "../i18n/monitor";
import { MonitorPage } from "./page";

// The page is public and shows itself at a glance: the theme follows the device, and there
// is nothing to keep in this browser (the panel's own theme is the admin's).
function applyTheme() {
  const dark = window.matchMedia?.("(prefers-color-scheme: dark)").matches;
  const root = document.documentElement;
  root.dataset.theme = dark ? "midnight" : "mikan";
  root.dataset.tone = dark ? "dark" : "light";
  const bg = getComputedStyle(root).getPropertyValue("--bg").trim();
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", bg || "#fbf1e8");
}
applyTheme();

// Dictionaries load before the first render: t() stays synchronous everywhere.
void initI18n(monitorDicts).then(() => {
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <ErrorBoundary>
        <M3eRoot sync>
          <MonitorPage />
        </M3eRoot>
      </ErrorBoundary>
    </StrictMode>,
  );
});
