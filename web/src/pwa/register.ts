export const PWA_UPDATE_EVENT = "tp:pwa-update-available";

export type PwaUpdateDetail = {
  registration: ServiceWorkerRegistration;
};

let waitingRegistration: ServiceWorkerRegistration | null = null;
let reloadAfterActivation = false;

function announceUpdate(registration: ServiceWorkerRegistration) {
  if (!registration.waiting) return;
  waitingRegistration = registration;
  window.dispatchEvent(new CustomEvent<PwaUpdateDetail>(PWA_UPDATE_EVENT, { detail: { registration } }));
}

function watchRegistration(registration: ServiceWorkerRegistration) {
  if (registration.waiting && navigator.serviceWorker.controller) {
    announceUpdate(registration);
  }
  registration.addEventListener("updatefound", () => {
    const worker = registration.installing;
    if (!worker) return;
    worker.addEventListener("statechange", () => {
      if (worker.state === "installed" && navigator.serviceWorker.controller) {
        announceUpdate(registration);
      }
    });
  });
}

export async function registerPwa(): Promise<ServiceWorkerRegistration | null> {
  if (!("serviceWorker" in navigator)) return null;
  try {
    const registration = await navigator.serviceWorker.register("/sw.js", {
      scope: "/",
      updateViaCache: "none",
    });
    watchRegistration(registration);
    return registration;
  } catch {
    // A missing/disabled service worker must never prevent the vault UI from
    // loading. The app remains a normal network-only web app in that case.
    return null;
  }
}

export function dismissPwaUpdate(): void {
  waitingRegistration = null;
}

export function activatePwaUpdate(): boolean {
  const waiting = waitingRegistration?.waiting;
  if (!waiting) return false;

  reloadAfterActivation = true;
  const reload = () => {
    if (!reloadAfterActivation) return;
    reloadAfterActivation = false;
    window.location.reload();
  };
  navigator.serviceWorker.addEventListener("controllerchange", reload, { once: true });
  waiting.postMessage({ type: "SKIP_WAITING" });
  return true;
}
