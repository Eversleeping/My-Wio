import React from "react";
import ReactDOM from "react-dom/client";
import { registerSW } from "virtual:pwa-register";
import App from "./App";
import { I18nProvider } from "./i18n";
import "./styles.css";

const frontendVersionValue = document.querySelector<HTMLMetaElement>('meta[name="wio-frontend-version"]')?.content ?? "";
const frontendVersion = frontendVersionValue && !frontendVersionValue.startsWith("__") ? frontendVersionValue : "";
let reloadingForUpdate = false;

async function reloadForFrontendUpdate() {
  if (reloadingForUpdate) return;
  reloadingForUpdate = true;
  try {
    if ("serviceWorker" in navigator) {
      const registrations = await navigator.serviceWorker.getRegistrations();
      await Promise.all(registrations.map(registration => registration.unregister()));
    }
    if ("caches" in window) {
      const keys = await caches.keys();
      await Promise.all(keys.map(key => caches.delete(key)));
    }
  } finally {
    window.location.reload();
  }
}

async function checkFrontendVersion() {
  if (!frontendVersion || reloadingForUpdate) return;
  try {
    const response = await fetch("/api/health", { cache: "no-store", credentials: "same-origin" });
    if (!response.ok) return;
    const health = await response.json() as { frontend_version?: string };
    if (health.frontend_version && health.frontend_version !== frontendVersion) await reloadForFrontendUpdate();
  } catch {
    // A temporary network failure should not disrupt an active console session.
  }
}

registerSW({
  immediate: true,
  onRegisteredSW: (_url, registration) => {
    window.setInterval(() => void registration?.update(), 5 * 60 * 1000);
  }
});
window.setInterval(() => void checkFrontendVersion(), 30 * 1000);
window.addEventListener("focus", () => void checkFrontendVersion());
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") void checkFrontendVersion();
});
void checkFrontendVersion();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <I18nProvider><App /></I18nProvider>
  </React.StrictMode>
);
