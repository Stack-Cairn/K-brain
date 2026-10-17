package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// ImageJobs is the public service contract. Execution and recovery belong to
// imaging.Service; HTTP requests never own the lifetime of accepted jobs.
type ImageJobs interface {
	CreateJob(context.Context, imaging.Scope, string, imaging.CreateJobRequest) (imaging.ImageJob, bool, error)
	GetJob(context.Context, imaging.Scope, string) (imaging.ImageJob, error)
	ListJobs(context.Context, imaging.Scope, string, imaging.PageRequest) (imaging.Page[imaging.ImageJob], error)
	CancelJob(context.Context, imaging.Scope, string) (imaging.ImageJob, error)
	ReadEvents(context.Context, imaging.Scope, string, uint64, int) ([]imaging.ImageEvent, error)
}

var _ ImageJobs = (*imaging.Service)(nil)
var _ ImageAssets = (*imaging.AssetStore)(nil)

type ImageAssets interface {
	ImportAsset(context.Context, imaging.Scope, io.Reader, string) (imaging.ImageAsset, error)
	OpenAsset(context.Context, imaging.Scope, string, string) (io.ReadCloser, imaging.ImageAsset, error)
}

// ImageAuthorization is issued only by trusted server-side authentication.
// CanConfigure grants settings and secret management, independently of execution.
type ImageAuthorization struct {
	Scope        imaging.Scope
	CanConfigure bool
}

// ImageScopeResolver verifies any delegated identity and its configuration grant.
// It runs only after the configured backend Bearer token has been verified.
type ImageScopeResolver func(*http.Request) (ImageAuthorization, error)

// ImageModule dependencies are assembled by the process owner. Store must be
// the same store used by Jobs so snapshots and event positions are atomic.
// The process owner starts/recovers and closes Jobs before closing Assets/Store.
type ImageModule struct {
	Verification *ImageVerificationManager
	Settings     *ImageSettingsManager
	// Profiles is the simplified configuration layer. When set, the image
	// configuration routes serve it instead of the legacy settings document.
	Profiles             *ImageProfilesManager
	Store                *imaging.Store
	Assets               ImageAssets
	Jobs                 ImageJobs
	AllowLocalImageScope bool
	ResolveScope         ImageScopeResolver
}

func imageWireVersion() string { return imaging.WireVersion }
func imageUnauthorized(w http.ResponseWriter, r *http.Request) {
	imageHTTPError(w, r, imageError(imaging.CodeAuthRequired))
}
func isImageBodyTooLarge(err error) bool { var e *http.MaxBytesError; return errors.As(err, &e) }

type imageRequestIDKey struct{}

func imageRequest(r *http.Request) *http.Request {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return r.WithContext(context.WithValue(r.Context(), imageRequestIDKey{}, hex.EncodeToString(id[:])))
}
func imageJSON(w http.ResponseWriter, r *http.Request, status int, fields map[string]any) {
	fields["version"] = imaging.WireVersion
	raw, err := json.Marshal(fields)
	if err != nil {
		imageHTTPError(w, r, imageError(imaging.CodeUnavailable))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(raw, '\n'))
}
func imageFailure(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool, details *imaging.ErrorDetails) {
	id, _ := r.Context().Value(imageRequestIDKey{}).(string)
	// Use a wire struct to preserve HTTP-only codes without exposing error causes.
	imageJSON(w, r, status, map[string]any{"error": struct {
		Code      string                `json:"code"`
		Message   string                `json:"message"`
		Retryable bool                  `json:"retryable"`
		RequestID string                `json:"requestId"`
		Details   *imaging.ErrorDetails `json:"details,omitempty"`
	}{code, message, retryable, id, details}})
}
func imageHTTPError(w http.ResponseWriter, r *http.Request, err error) {
	code := imaging.CodeUnavailable
	var details *imaging.ErrorDetails
	var ie *imaging.ImageError
	var tooLarge *http.MaxBytesError
	if errors.As(err, &ie) {
		code = ie.Code
		if code == imaging.CodeEventGap {
			details = ie.Details
		}
	} else if errors.As(err, &tooLarge) {
		code = imaging.CodeTooLarge
	} else if errors.Is(err, imaging.ErrUnsupported) {
		code = imaging.CodeUnsupportedOperation
	} else if strings.HasPrefix(err.Error(), imaging.CodeInvalidInput+":") {
		code = imaging.CodeInvalidInput
	}
	status := http.StatusServiceUnavailable
	switch code {
	case imaging.CodeInvalidInput, imaging.CodeUnsupportedOperation:
		status = http.StatusUnprocessableEntity
	case imaging.CodeAuthRequired:
		status = http.StatusUnauthorized
	case imaging.CodeForbidden:
		status = http.StatusForbidden
	case imaging.CodeNotFound:
		status = http.StatusNotFound
	case imaging.CodeRevisionConflict, imaging.CodeIdempotencyConflict, imaging.CodeConnectionInUse, imaging.CodeEventGap, imaging.CodeProjectBusy:
		status = http.StatusConflict
	case imaging.CodeTooLarge:
		status = http.StatusRequestEntityTooLarge
	case imaging.CodeRateLimited:
		status = http.StatusTooManyRequests
	default:
		code = imaging.CodeUnavailable
	}
	message := imaging.PublicError(&imaging.ImageError{Code: code}).Message
	if code == imaging.CodeProjectBusy {
		message = "Image project has active jobs"
	}
	imageFailure(w, r, status, code, message, status == 503 || status == 429, details)
}
func imageMethod(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	imageFailure(w, r, http.StatusMethodNotAllowed, "IMAGE_METHOD_NOT_ALLOWED", "Image method is not allowed", false, nil)
	return false
}
func imageDecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		imageHTTPError(w, r, imageError(imaging.CodeInvalidInput))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	// Reject null in addition to unknown fields and trailing JSON values.
	var raw json.RawMessage
	if err = d.Decode(&raw); err == nil && (len(raw) == 0 || raw[0] != '{') {
		err = imageError(imaging.CodeInvalidInput)
	}
	if err == nil {
		var extra any
		if e := d.Decode(&extra); e != io.EOF {
			if e != nil {
				err = e
			} else {
				err = imageError(imaging.CodeInvalidInput)
			}
		}
	}
	if err == nil {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		err = dec.Decode(dst)
	}
	if err != nil {
		var large *http.MaxBytesError
		if !errors.As(err, &large) {
			err = imageError(imaging.CodeInvalidInput)
		}
		imageHTTPError(w, r, err)
		return false
	}
	return true
}
func imagePage(r *http.Request) (imaging.PageRequest, error) {
	p := imaging.PageRequest{Cursor: r.URL.Query().Get("cursor"), Limit: 30}
	if r.URL.Query().Has("limit") {
		n, e := strconv.Atoi(r.URL.Query().Get("limit"))
		if e != nil || n < 1 || n > 100 {
			return p, imageError(imaging.CodeInvalidInput)
		}
		p.Limit = n
	}
	return p, nil
}
func (s *Server) resolveImageScope(r *http.Request) (ImageAuthorization, error) {
	denied := imageError(imaging.CodeForbidden)
	if s.token == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || !s.authorized(r) {
		return ImageAuthorization{}, denied
	}
	if s.image.ResolveScope != nil {
		auth, err := s.image.ResolveScope(r)
		if err != nil {
			return ImageAuthorization{}, err
		}
		if auth.Scope.OwnerID == "" || auth.Scope.BackendID == "" {
			return ImageAuthorization{}, denied
		}
		return auth, nil
	}
	// A forwarded/delegated request must never fall back to the desktop owner
	// when the verifier has not been installed.
	for _, header := range []string{"X-Kbrain-Image-Delegation", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host"} {
		if len(r.Header.Values(header)) > 0 {
			return ImageAuthorization{}, denied
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if !s.image.AllowLocalImageScope || ip == nil || !ip.IsLoopback() {
		return ImageAuthorization{}, denied
	}
	return ImageAuthorization{Scope: imaging.Scope{OwnerID: "local", BackendID: "local"}, CanConfigure: true}, nil
}

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	if s.image == nil {
		imageHTTPError(w, r, imageError(imaging.CodeUnavailable))
		return
	}
	authorization, err := s.resolveImageScope(r)
	if err != nil {
		imageHTTPError(w, r, err)
		return
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		imageHTTPError(w, r, imageError(imaging.CodeUnavailable))
		return
	}
	scope := authorization.Scope
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/image/"), "/")
	// Profiles are a read-only view of chat providers without key values, so
	// listing them needs only execute access, like the old target list.
	if (parts[0] == "settings" || parts[0] == "secrets") && !authorization.CanConfigure {
		imageHTTPError(w, r, imageError(imaging.CodeForbidden))
		return
	}
	if parts[0] == "targets" && len(parts) > 1 {
		s.imageVerification(w, r, authorization, parts)
		return
	}
	switch parts[0] {
	case "profiles":
		if s.image.Profiles == nil {
			imageHTTPError(w, r, imageError(imaging.CodeUnavailable))
			return
		}
		s.image.Profiles.routes(w, r, scope, parts)
		return
	case "platforms":
		if s.image.Profiles == nil {
			imageHTTPError(w, r, imageError(imaging.CodeUnavailable))
			return
		}
		if !imageMethod(w, r, http.MethodGet) {
			return
		}
		imageJSON(w, r, 200, map[string]any{"platforms": s.image.Profiles.Platforms()})
		return
	case "settings", "targets", "secrets":
		if len(parts) != 1 {
			break
		}
		s.imageConfiguration(w, r, scope, parts[0])
		return
	case "projects":
		s.imageProjects(w, r, scope, parts)
		return
	case "assets":
		s.imageAssets(w, r, scope, parts)
		return
	case "jobs":
		s.imageJobs(w, r, scope, parts)
		return
	}
	imageHTTPError(w, r, imageError(imaging.CodeNotFound))
}
func (s *Server) imageConfiguration(w http.ResponseWriter, r *http.Request, scope imaging.Scope, route string) {
	m := s.image.Settings
	if m == nil {
		imageHTTPError(w, r, imageError(imaging.CodeUnavailable))
		return
	}
	switch route {
	case "settings":
		if !imageMethod(w, r, http.MethodGet, http.MethodPut) {
			return
		}
		var settings imaging.ImageSettings
		var err error
		if r.Method == http.MethodGet {
			settings, err = m.ImageSettings(scope)
		} else {
			var input struct {
				ExpectedRevision *uint64                   `json:"expectedRevision"`
				Connections      []imaging.ImageConnection `json:"connections"`
				Targets          []imaging.ImageTarget     `json:"targets"`
			}
			if !imageDecode(w, r, &input) {
				return
			}
			if input.ExpectedRevision == nil || *input.ExpectedRevision > imaging.MaxSafeInteger {
				imageHTTPError(w, r, imageError(imaging.CodeInvalidInput))
				return
			}
			settings, err = m.UpdateImageSettings(r.Context(), scope, imaging.ImageSettingsUpdate{ExpectedRevision: *input.ExpectedRevision, Connections: input.Connections, Targets: input.Targets})
		}
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		imageJSON(w, r, 200, map[string]any{"settings": settings})
	case "targets":
		if !imageMethod(w, r, http.MethodGet) {
			return
		}
		settings, err := m.ImageSettings(scope)
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		imageJSON(w, r, 200, map[string]any{"items": settings.Targets})
	case "secrets":
		if !imageMethod(w, r, http.MethodPost) {
			return
		}
		var input imaging.ImageSecretInput
		if !imageDecode(w, r, &input) {
			return
		}
		result, err := m.WriteImageSecret(r.Context(), scope, input)
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		imageJSON(w, r, 200, map[string]any{"ref": result.Ref, "configured": result.Configured})
	}
}
