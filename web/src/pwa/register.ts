export const PWA_UPDATE_EVENT = "tp:pwa-update-available";
export const PWA_INSTALL_STATE_EVENT = "tp:pwa-install-state";

export type PwaInstallState = "unavailable" | "available" | "installed";

export type PwaInstallStateDetail = {
  state: PwaInstallState;
};

export type PwaInstallOutcome = "accepted" | "dismissed" | "unavailable";

type BeforeInstallPromptEvent = Event & {
  prompt: () => Promise<void>;
  userChoice: Promise<{ outcome: "accepted" | "dismissed"; platform: string }>;
};

export type PwaUpdateDetail = {
  registration: ServiceWorkerRegistration;
};

let waitingRegistration: ServiceWorkerRegistration | null = null;
let reloadAfterActivation = false;
let deferredInstallPrompt: BeforeInstallPromptEvent | null = null;
let installState: PwaInstallState = "unavailable";
let installListenersReady = false;

function isStandaloneDisplayMode(): boolean {
  if (typeof window === "undefined") return false;
  const standaloneNavigator = navigator as Navigator & { standalone?: boolean };
  return Boolean(standaloneNavigator.standalone) || window.matchMedia?.("(display-mode: standalone)").matches === true;
}

function announceInstallState(state: PwaInstallState): void {
  installState = state;
  if (typeof window === "undefined") return;
  window.dispatchEvent(
    new CustomEvent<PwaInstallStateDetail>(PWA_INSTALL_STATE_EVENT, { detail: { state } }),
  );
}

function initializeInstallPrompt(): void {
  if (installListenersReady || typeof window === "undefined") return;
  installListenersReady = true;
  installState = isStandaloneDisplayMode() ? "installed" : "unavailable";

  window.addEventListener("beforeinstallprompt", (event) => {
    event.preventDefault();
    deferredInstallPrompt = event as BeforeInstallPromptEvent;
    announceInstallState("available");
  });
  window.addEventListener("appinstalled", () => {
    deferredInstallPrompt = null;
    announceInstallState("installed");
  });
}

initializeInstallPrompt();

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
  initializeInstallPrompt();
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

export function getPwaInstallState(): PwaInstallState {
  if (isStandaloneDisplayMode()) return "installed";
  return installState;
}

/**
 * Opens the browser's native install dialog. This must be called from a
 * user-initiated event; browsers intentionally reject background prompts.
 */
export async function promptPwaInstall(): Promise<PwaInstallOutcome> {
  const promptEvent = deferredInstallPrompt;
  if (!promptEvent) return "unavailable";

  deferredInstallPrompt = null;
  announceInstallState("unavailable");
  try {
    await promptEvent.prompt();
    const choice = await promptEvent.userChoice;
    if (choice.outcome === "accepted") {
      announceInstallState("installed");
    }
    return choice.outcome;
  } catch {
    return "unavailable";
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
