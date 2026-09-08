# Bitwarden JSON Import Design

## Goal

Add a server-side Bitwarden JSON import flow to the existing personal
transfer page. The import must be append-only, previewable before any
database write, and safe for the encrypted vault model already used by the
application.

The first version accepts the unencrypted JSON export produced by Bitwarden.
Encrypted Bitwarden exports and CSV exports are outside this version.

## User-visible behavior

The transfer page gets a Bitwarden JSON upload control alongside the existing
Tiny Password archive import. A user selects a JSON export, requests a
preview, reviews counts and any conversion warnings, and explicitly confirms
the import. Canceling or abandoning the preview removes staged plaintext.

All imported records become personal records owned by the authenticated user.
Existing records are never updated or deleted. Bitwarden identifiers are
external identifiers and are not reused as vault row IDs; every imported
record receives a fresh UUIDv7.

The preview response continues to use the existing `preview_token`, `counts`,
`conflicts`, and `missing_references` shape. Bitwarden imports have no internal
vault references and therefore report zero conflicts and no missing
references. A successful confirmation returns the existing import summary and
creates one audit event.

## Supported conversion

Bitwarden item types 1 through 4 are converted as follows:

| Bitwarden type | Tiny Password type | Mapping |
| --- | --- | --- |
| 1, Login | `login` | `name`, username, password, login URIs, notes, and favorite |
| 2, Secure note | `secure_note` | `name` and notes as the note body |
| 3, Card | `credit_card` | cardholder, number, expiration month/year, security code, name, and notes |
| 4, Identity | `identity` | full name, company, phone, email, country, state, city, address, postal code, name, and notes |

Bitwarden folder names are added as tags. Folder IDs must resolve to the
folder table in the same export. Multiple folders are not represented by the
Bitwarden format for one item and result in one tag at most.

The target schema has no fields for TOTP, FIDO2 credentials, Bitwarden custom
fields, identity document numbers, card brand, or attachment metadata. These
values are serialized into a clearly labeled block appended to the item's
notes. This preserves the secret values without introducing a new vault field
system. The block is deterministic, uses JSON encoding for values, and is
included only when a source value is present. Actual attachment contents are
not present in a normal Bitwarden JSON export and are not downloaded.

Bitwarden SSH-key items (type 5) and unknown item types are rejected as an
invalid import rather than silently skipped, because the first version must
not report success while losing records. The file is rejected atomically if
any item cannot be converted or violates the target field limits. Empty
Bitwarden notes are represented as empty note bodies by relaxing only the
server-side body-presence requirement for secure notes; the existing browser
editor may still require text for newly authored notes.

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
with bounded input, verifies `encrypted` is false, validates the root and
item shapes, resolves folders, produces normalized payload JSON, and uses the
same exported payload and tag validators used by archive imports. It never
logs or returns secret values in errors. The HTTP layer maps parser failures
to the existing `BAD_ARCHIVE` safe error response.

The internal import item gains a `Favorite` field. Archive payloads carry this
field optionally so old Tiny Password archives remain readable; Bitwarden
favorites are retained on the inserted rows. Bitwarden imports leave
`OriginalID` empty so the vault service allocates fresh IDs and no external
identifier can collide with a local row.

## Error handling and security

- JSON syntax errors, encrypted exports, missing required sections, duplicate
  source IDs, unknown item types, unresolved folders, and target-limit
  violations reject the entire preview.
- A file with zero items is rejected.
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

Add unit tests for valid mixed exports, all field mappings, folder tags,
optional unsupported-field preservation, empty notes, encrypted exports,
unknown types, malformed folders, duplicate IDs, and target-limit failures.
Add integration coverage for the multipart endpoint, preview-without-write,
confirmation, favorite preservation, append-only behavior, and safe error
responses. Add frontend tests for selecting a JSON file, showing the preview,
confirming it, cancellation, and request/session lifecycle behavior.

Update OpenAPI with the endpoint and Bitwarden-specific request description,
and update the transfer page copy so users know that encrypted Bitwarden
exports and CSV are not supported in this version.
