# Bitwarden JSON Import Design

## Goal

Add a server-side Bitwarden JSON import flow to the existing personal
transfer page. The import must be append-only, previewable before any
database write, and safe for the encrypted vault model already used by the
application.

The first version accepts the unencrypted JSON export produced by Bitwarden.
Encrypted Bitwarden exports and CSV exports are outside this version.
This importer is credential-only: it imports Bitwarden Login items and does
not recreate Bitwarden account data or other vault item categories.

## User-visible behavior

The transfer page gets a Bitwarden JSON upload control alongside the existing
Tiny Password archive import. A user selects a JSON export, requests a
preview, reviews counts and any conversion warnings, and explicitly confirms
the import. Canceling or abandoning the preview removes staged plaintext.

Only Bitwarden Login items become personal records owned by the authenticated
user. Existing records are never updated or deleted. Bitwarden identifiers are
external identifiers and are not reused as vault row IDs; every imported
record receives a fresh UUIDv7.

The preview response continues to use the existing `preview_token`, `counts`,
`conflicts`, and `missing_references` shape. Bitwarden imports have no internal
vault references and therefore report zero conflicts and no missing
references. A successful confirmation returns the existing import summary and
creates one audit event.

## Supported conversion

Only Bitwarden item type 1 (Login) is converted:

| Bitwarden type | Tiny Password type | Mapping |
| --- | --- | --- |
| 1, Login | `login` | item name, username, password, and non-empty login URLs |

Secure notes, cards, identities, SSH keys, unknown item types, item notes,
TOTP, FIDO2 credentials, custom fields, attachments, folder tags, and favorite
flags are skipped. Bitwarden's own account profile, master password, sessions,
and account settings are not vault items and are not imported. An export with
no Login items is rejected rather than producing a no-op preview.

## API and data flow

Add a dedicated endpoint:

`POST /api/v1/transfer/import/bitwarden/preview`

It accepts authenticated `multipart/form-data` with a `file` part. The same
96 MiB request and 64 MiB file limits as archive import apply. The endpoint
does not accept a passphrase. It returns the existing preview response and
stages normalized `vault.ImportItem` values in the restricted transfer work
directory.

The existing `/transfer/import/confirm` and `/transfer/import/cancel`
endpoints remain format-neutral: their signed, session-bound token points to
the normalized staged items, so the same one-shot claim and transaction logic
handles both archive and Bitwarden previews. The staging refactor will put
common preview creation behind one helper rather than duplicating token or
cleanup logic.

The Bitwarden parser is a pure package-level component. It decodes the JSON
with bounded input, verifies `encrypted` is false, selects Login items,
produces normalized payload JSON, and uses the same exported payload validator
used by archive imports. It never logs or returns secret values in errors. The
HTTP layer maps parser failures to the existing `BAD_ARCHIVE` safe error
response.

The internal import item gains a `Favorite` field. Archive payloads carry this
field optionally so old Tiny Password archives remain readable. Bitwarden
imports leave `OriginalID` empty and do not set tags or favorite state, so the
vault service allocates fresh IDs without importing unrelated metadata.

## Error handling and security

- JSON syntax errors, encrypted exports, missing required sections, duplicate
  Login IDs, missing Login data, no Login items, and target-limit violations
  reject the entire preview. Non-Login item types are ignored.
- A file with zero source items or zero Login items is rejected.
- The request body and uploaded file are bounded before parsing; temporary
  files and staged plaintext are mode 0600 under the existing restricted work
  directory.
- No database writes happen during preview. Confirmation retains the existing
  atomic claim, live-session checks, transaction, audit, and commit-unknown
  behavior.
- Source names and values are never interpolated into errors or logs.
- Imported records are personal only, regardless of Bitwarden organization or
  collection metadata; organization ownership is not recreated.

## Testing and documentation

Add unit tests for valid mixed exports, credential field mappings, omission of
unsupported Login fields, skipped non-Login types, encrypted exports, duplicate
Login IDs, and target-limit failures.
Add integration coverage for the multipart endpoint, preview-without-write,
confirmation, append-only behavior, metadata omission, and safe error
responses. Add frontend tests for selecting a JSON file, showing the preview,
confirming it, cancellation, and request/session lifecycle behavior.

Update OpenAPI with the endpoint and Bitwarden-specific request description,
and update the transfer page copy so users know that encrypted Bitwarden
exports and CSV are not supported in this version.
