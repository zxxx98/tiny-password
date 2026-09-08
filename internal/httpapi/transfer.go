package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/platform/archive"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/transfer"
	"github.com/tiny-password/tiny-password/internal/vault"
)

// TransferDeps wires the personal import/export endpoints (T20).
type TransferDeps struct {
	Service *transfer.Service
	Session *auth.Service
}

func registerTransfer(api *http.ServeMux, deps TransferDeps) {
	guarded := func(handler http.HandlerFunc) http.Handler {
		return RequireSession(deps.Session, false, handler)
	}

	// Export streams the caller's encrypted 7z archive. The passphrase stays
	// in the request body; the archive is cleaned up after streaming.
	api.Handle("POST /api/v1/transfer/export", guarded(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Passphrase        string `json:"passphrase"`
			PassphraseConfirm string `json:"passphrase_confirm"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		path, cleanup, err := deps.Service.Export(r.Context(), CurrentPrincipal(r.Context()), transfer.ExportInput{
			Passphrase:        input.Passphrase,
			PassphraseConfirm: input.PassphraseConfirm,
		})
		if err != nil {
			writeTransferError(w, r, err)
			return
		}
		defer cleanup()
		file, err := os.Open(path)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the archive could not be read")
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the archive could not be read")
			return
		}
		w.Header().Set("Content-Type", "application/x-7z-compressed")
		w.Header().Set("Content-Disposition", `attachment; filename="tiny-password-export.7z"`)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.Copy(w, file)
	}))

	api.Handle("POST /api/v1/transfer/import/preview", guarded(func(w http.ResponseWriter, r *http.Request) {
		// The archive arrives as multipart/form-data; the passphrase is a
		// form field (never in the URL, D04).
		if r.ContentLength > archive.MaxTransferRequestBytes {
			writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "the archive exceeds the allowed size")
			return
		}
		// ParseMultipartForm's argument only controls when file parts spill to
		// temporary files. Bound the complete request first so chunked uploads
		// cannot bypass the limit.
		r.Body = http.MaxBytesReader(w, r.Body, archive.MaxTransferRequestBytes)
		if err := r.ParseMultipartForm(archive.MaxArchiveBytes); err != nil {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
			writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "the archive exceeds the allowed size")
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		passphrase := r.FormValue("passphrase")
		if len(passphrase) > transfer.MaxPassphraseBytes {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "passphrase exceeds the allowed size")
			return
		}
		file, _, err := r.FormFile("archive")
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "archive file is required")
			return
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, int64(archive.MaxArchiveBytes)+1))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "archive could not be read")
			return
		}
		if len(raw) > archive.MaxArchiveBytes {
			writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "the archive exceeds the allowed size")
			return
		}
		result, err := deps.Service.Preview(r.Context(), CurrentPrincipal(r.Context()), raw, passphrase)
		if err != nil {
			writeTransferError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))

	api.Handle("POST /api/v1/transfer/import/cancel", guarded(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			PreviewToken string `json:"preview_token"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		if err := deps.Service.CancelPreview(r.Context(), CurrentPrincipal(r.Context()), input.PreviewToken); err != nil {
			writeTransferError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	api.Handle("POST /api/v1/transfer/import/confirm", guarded(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			PreviewToken string `json:"preview_token"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		imported, remapped, err := deps.Service.Confirm(r.Context(), CurrentPrincipal(r.Context()), input.PreviewToken)
		if err != nil {
			writeTransferError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"imported_count":      imported,
			"remapped_references": remapped,
		})
	}))
}

func writeTransferError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, vault.ErrImportCommitUnknown):
		// A commit error is ambiguous: SQLite may have committed the import.
		// Never tell the client to retry a one-shot operation.
		writeError(w, r, http.StatusInternalServerError, "IMPORT_OUTCOME_UNKNOWN", "the import outcome is uncertain; do not retry")
	case errors.Is(err, vault.ErrExportTooLarge):
		writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "the export exceeds the allowed size")
	case errors.Is(err, archive.ErrWrongPassphrase):
		writeError(w, r, http.StatusBadRequest, "WRONG_PASSPHRASE", "the archive passphrase is wrong")
	case errors.Is(err, archive.ErrTooLarge):
		writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "the archive exceeds the allowed size")
	case errors.Is(err, transfer.ErrInvalidInput):
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid transfer input")
	case errors.Is(err, archive.ErrBadArchive):
		writeError(w, r, http.StatusBadRequest, "BAD_ARCHIVE", "the archive is invalid")
	case errors.Is(err, vault.ErrPayloadInvalid), errors.Is(err, vault.ErrReferenceForbidden):
		writeError(w, r, http.StatusBadRequest, "BAD_ARCHIVE", "the archive is invalid")
	case sqlite.IsBusy(err), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		w.Header().Set("Retry-After", "1")
		writeError(w, r, http.StatusServiceUnavailable, "DATABASE_BUSY", "the transfer is temporarily unavailable; retry later")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the transfer request could not be completed")
	}
}
