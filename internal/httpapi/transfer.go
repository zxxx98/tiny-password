package httpapi

import (
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/platform/archive"
	"github.com/tiny-password/tiny-password/internal/transfer"
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
		raw, err := os.ReadFile(path)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the archive could not be read")
			return
		}
		w.Header().Set("Content-Type", "application/x-7z-compressed")
		w.Header().Set("Content-Disposition", `attachment; filename="tiny-password-export.7z"`)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}))

	api.Handle("POST /api/v1/transfer/import/preview", guarded(func(w http.ResponseWriter, r *http.Request) {
		// The archive arrives as multipart/form-data; the passphrase is a
		// form field (never in the URL, D04).
		if err := r.ParseMultipartForm(archive.MaxArchiveBytes); err != nil {
			writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "the archive exceeds the allowed size")
			return
		}
		passphrase := r.FormValue("passphrase")
		file, _, err := r.FormFile("archive")
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "archive file is required")
			return
		}
		defer file.Close()
		raw, err := io.ReadAll(file)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "archive could not be read")
			return
		}
		result, err := deps.Service.Preview(r.Context(), CurrentPrincipal(r.Context()), raw, passphrase)
		if err != nil {
			writeTransferError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
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
	case errors.Is(err, archive.ErrWrongPassphrase):
		writeError(w, r, http.StatusBadRequest, "WRONG_PASSPHRASE", "the archive passphrase is wrong")
	case errors.Is(err, archive.ErrTooLarge):
		writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "the archive exceeds the allowed size")
	default:
		// BAD_ARCHIVE and validation failures carry safe, value-free text.
		writeError(w, r, http.StatusBadRequest, "BAD_ARCHIVE", err.Error())
	}
}
