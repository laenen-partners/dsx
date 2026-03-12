package fileupload

import (
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	dsx "github.com/laenen-partners/dsx"
	"github.com/laenen-partners/dsx/utils"
	"github.com/laenen-partners/dsx/utils/iox"
	"github.com/starfederation/datastar-go/datastar"
)

// HandlerOption configures upload validation.
type HandlerOption func(*handlerConfig)

type handlerConfig struct {
	maxFileSize  int64    // per-file limit in bytes (default 10MB)
	allowedTypes []string // allowed MIME prefixes (empty = all)
	maxFiles     int      // max total files per component (default 10)
}

// WithMaxFileSize sets the maximum allowed size per file in bytes.
func WithMaxFileSize(bytes int64) HandlerOption {
	return func(c *handlerConfig) { c.maxFileSize = bytes }
}

// WithAllowedTypes restricts uploads to files whose Content-Type starts
// with one of the given prefixes (e.g. "image/", "application/pdf").
func WithAllowedTypes(types ...string) HandlerOption {
	return func(c *handlerConfig) { c.allowedTypes = types }
}

// WithMaxFiles sets the maximum number of files allowed per component.
func WithMaxFiles(n int) HandlerOption {
	return func(c *handlerConfig) { c.maxFiles = n }
}

// StoreKey builds the composite key used to identify files in the store.
func StoreKey(sessionID, componentID string) string {
	return sessionID + ":" + componentID
}

// UploadPath is the standard handler path for file uploads.
const UploadPath = "/upload/files"

// RemovePath is the standard handler path for file removal.
const RemovePath = "/upload/remove"

// UploadHandler returns an http.HandlerFunc that accepts multipart file
// uploads and responds with an SSE patch of the updated file list.
//
// Mount at a dedicated POST path:
//
//	r.Post(fileupload.UploadPath, fileupload.UploadHandler(store))
func UploadHandler(store Store, opts ...HandlerOption) http.HandlerFunc {
	cfg := &handlerConfig{
		maxFileSize: 10 << 20, // 10MB
		maxFiles:    10,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		componentID := r.URL.Query().Get("id")
		if componentID == "" {
			http.Error(w, "missing id query parameter", http.StatusBadRequest)
			return
		}

		removeURL := r.URL.Query().Get("removeUrl")
		if removeURL != "" && !IsRelativePath(removeURL) {
			http.Error(w, "invalid removeUrl parameter", http.StatusBadRequest)
			return
		}

		wxctx := dsx.FromContext(r.Context())
		key := StoreKey(wxctx.SessionID, componentID)

		if err := r.ParseMultipartForm(32 << 20); err != nil {
			slog.Error("fileupload: parse form failed", "error", err)
			http.Error(w, "failed to parse upload", http.StatusBadRequest)
			return
		}

		existing := store.List(key)
		files := r.MultipartForm.File["files"]

		var errors []string
		for _, fh := range files {
			// Check max files
			if len(existing) >= cfg.maxFiles {
				errors = append(errors, fmt.Sprintf("maximum of %d files allowed", cfg.maxFiles))
				break
			}

			// Check file size
			if fh.Size > cfg.maxFileSize {
				errors = append(errors, fmt.Sprintf("%s exceeds maximum size", fh.Filename))
				continue
			}

			// Detect actual MIME type from file content (first 512 bytes).
			ct, detectErr := DetectMIME(fh)
			if detectErr != nil {
				slog.Error("fileupload: MIME detection failed", "file", fh.Filename, "error", detectErr)
				errors = append(errors, fmt.Sprintf("%s: unable to determine file type", fh.Filename))
				continue
			}

			if len(cfg.allowedTypes) > 0 {
				allowed := false
				for _, prefix := range cfg.allowedTypes {
					if strings.HasPrefix(ct, prefix) {
						allowed = true
						break
					}
				}
				if !allowed {
					errors = append(errors, fmt.Sprintf("%s: type %s not allowed", fh.Filename, ct))
					continue
				}
			}

			meta := FileMeta{
				ID:       utils.RandomID(),
				Name:     fh.Filename,
				Size:     fh.Size,
				MimeType: ct,
			}
			store.Add(key, meta)
			existing = append(existing, meta)
		}

		sse := datastar.NewSSE(w, r)
		if err := sse.PatchElementTempl(
			fileListItems(componentID, existing, removeURL),
			datastar.WithSelectorID(componentID+"-list"),
			datastar.WithModeInner(),
		); err != nil {
			return
		}

		if len(errors) > 0 {
			_ = sse.PatchElements(
				fmt.Sprintf(`<p class="text-error text-sm">%s</p>`, strings.Join(errors, "; ")),
				datastar.WithSelectorID(componentID+"-errors"),
				datastar.WithModeInner(),
			)
		} else {
			_ = sse.PatchElements(
				"",
				datastar.WithSelectorID(componentID+"-errors"),
				datastar.WithModeInner(),
			)
		}
	}
}

// RemoveHandler returns an http.HandlerFunc that removes a file from the
// store and responds with an SSE patch of the updated file list.
//
// Mount at a dedicated POST path:
//
//	r.Post(fileupload.RemovePath, fileupload.RemoveHandler(store))
func RemoveHandler(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		componentID := r.URL.Query().Get("id")
		fileID := r.URL.Query().Get("fileId")
		removeURL := r.URL.Query().Get("removeUrl")
		if componentID == "" || fileID == "" {
			http.Error(w, "missing id or fileId query parameter", http.StatusBadRequest)
			return
		}
		if removeURL != "" && !IsRelativePath(removeURL) {
			http.Error(w, "invalid removeUrl parameter", http.StatusBadRequest)
			return
		}

		wxctx := dsx.FromContext(r.Context())
		key := StoreKey(wxctx.SessionID, componentID)

		store.Remove(key, fileID)
		files := store.List(key)

		sse := datastar.NewSSE(w, r)
		_ = sse.PatchElementTempl(
			fileListItems(componentID, files, removeURL),
			datastar.WithSelectorID(componentID+"-list"),
			datastar.WithModeInner(),
		)
	}
}

// DetectMIME opens a multipart file header and reads the first 512 bytes
// to detect the actual MIME type using http.DetectContentType.
func DetectMIME(fh *multipart.FileHeader) (str string, err error) {
	f, err := fh.Open()
	if err != nil {
		return "", fmt.Errorf("opening file: %w", err)
	}
	defer iox.CloseAndCapture(f, &err)

	buf := make([]byte, 512)
	n, err := io.ReadAtLeast(f, buf, 1)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("reading file header: %w", err)
	}
	return http.DetectContentType(buf[:n]), nil
}

// IsRelativePath validates that a URL string is a relative path (starts with /)
// and does not contain a scheme or protocol-relative prefix.
func IsRelativePath(s string) bool {
	if s == "" {
		return false
	}
	if !strings.HasPrefix(s, "/") {
		return false
	}
	// Reject protocol-relative URLs (e.g. "//evil.com")
	if strings.HasPrefix(s, "//") {
		return false
	}
	// Reject URLs with a scheme embedded (shouldn't happen with / prefix, but be safe)
	if u, err := url.Parse(s); err != nil || u.Host != "" {
		return false
	}
	return true
}

// Route returns a RouteOption that registers upload and remove handlers.
func Route(store Store, opts ...HandlerOption) func(chi.Router) {
	return func(r chi.Router) {
		r.Post(UploadPath, UploadHandler(store, opts...))
		r.Post(RemovePath, RemoveHandler(store))
	}
}

// RemoveQueryParams builds a remove URL with properly escaped query parameters.
func RemoveQueryParams(removeURL, componentID, fileID string) string {
	return fmt.Sprintf("%s?id=%s&fileId=%s&removeUrl=%s",
		removeURL,
		url.QueryEscape(componentID),
		url.QueryEscape(fileID),
		url.QueryEscape(removeURL),
	)
}
