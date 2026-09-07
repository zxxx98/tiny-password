# Changelog

## Unreleased

### Added

- Installable PWA manifest, self-contained offline explanation, generated static-only service-worker allowlist, and user-controlled update prompt.
- Explicit Compose deployment profiles for secure default isolation, LAN reverse-proxy binding, optional R2 credentials, and Cloudflare Tunnel token-file operation.
- Chinese deployment, secret-handling, Tunnel, backup/recovery, release-checklist, accessibility, security-header, secret-scan, search-benchmark, and restore-drill documentation/tools.
- Multi-architecture release workflow with amd64/arm64 runtime smoke tests, provenance/SBOM attestations, and high/critical vulnerability scanning.

### Security

- The default Compose service publishes no host port, runs non-root with dropped capabilities, read-only root filesystem, bounded resources, a tmpfs scratch area, and a graceful stop window.
- Service-worker navigations, API calls, archive transfers, and all non-whitelisted URLs remain network-only; the browser never receives an offline copy of vault contents.
