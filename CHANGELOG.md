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
