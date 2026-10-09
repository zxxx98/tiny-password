# Changelog

## Unreleased

### Added

- Installable PWA manifest, self-contained offline explanation, generated static-only service-worker allowlist, and user-controlled update prompt.
- Explicit Compose deployment profiles for secure default isolation, LAN reverse-proxy binding, optional R2 credentials, and Cloudflare Tunnel token-file operation.
- Chinese deployment, secret-handling, Tunnel, backup/recovery, release-checklist, accessibility, security-header, secret-scan, search-benchmark, and restore-drill documentation/tools.
- Multi-architecture release workflow with amd64/arm64 runtime smoke tests, provenance/SBOM attestations, and high/critical vulnerability scanning.
- Native app identity entries gained a one-tap `COPY ADDRESS` action that writes the same name/address/phone block the web vault copies, with a transient `COPIED` state.

### Security

- The default Compose service publishes no host port, runs non-root with dropped capabilities, read-only root filesystem, bounded resources, a tmpfs scratch area, and a graceful stop window.
- Service-worker navigations, API calls, archive transfers, and all non-whitelisted URLs remain network-only; the browser never receives an offline copy of vault contents.

## Mobile 1.4.1 (10) - 2026-10-09

### Changed

- Consolidate Android version information and update checks into a dedicated settings page, with compact settings entries on the sign-in and vault screens.
- Preserve login inputs, vault search and list position when returning from settings; hide settings while the app is in the background.

### Validation

- Mobile TypeScript checks and all 175 unit tests passed.

## Mobile 1.4.0 (9) - 2026-10-08

### Fixed

- Restore the signing identity used by previous APK releases to retain upgrade compatibility without signing Secrets.
- Retry APK publication when a version tag exists but no complete release APK has been published.

### Added

- Manual Android update checks on the sign-in and vault screens, showing the installed APK version and release notes before opening the APK download in a browser.
- Identify stable Android releases by tag and versionCode, excluding server releases and incomplete uploads, with pagination, timeout, network-error and rate-limit handling.

### Validation

- Mobile TypeScript checks, unit tests and Android debug Kotlin compilation passed.

## Mobile 1.3.0 (8) - 2026-10-08

### Added

- Opt-in fingerprint / biometric login backed by system-protected credentials, with password fallback and no automatic password filling or biometric prompt at startup.

### Security

- Bind biometric credentials to the configured server; remove saved credentials when disabling the feature, switching servers, or signing out.
- Refresh saved credentials after forced password changes and discard late authentication results; serialize credential writes so sign-out cannot be undone by pending enrollment.

## 1.0.1 - 2026-10-03

### Added

- Web vault sorting, combined filters, tag/scope/type grouping, bulk management, item duplication, password generation and strength feedback, health findings, quick copy, and keyboard shortcuts.
- Authorized metadata browsing and independently loaded tag options; business fields remain encrypted at rest and browse responses exclude credential payloads.

### Fixed

- Bind selection to its query results and block stale-row selection and bulk actions while replacement results load; retain selection during pagination and keep failed bulk items available for retry.
- Schedule clipboard cleanup and password-copy auditing after successful writes even when the quick-copy component has unmounted, while preserving later clipboard contents.
- Page timestamp sorts in SQL with page-sized decryption and retain only a bounded page of metadata during global encrypted-title sorting.

### Validation

- Web type checking and all 157 tests passed; relevant Go unit and integration tests and frontend/server builds passed.
- All 10 Chromium management and vault scenarios passed, including delayed filter responses, clipboard writes completing after unmount, and desktop/mobile layouts.
