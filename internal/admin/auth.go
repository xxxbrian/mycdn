package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/time/rate"
)

const sessionCookieName = "mycdn_session"

// Auth owns process-local management sessions. The composition root closes it
// after draining HTTP handlers; neither the key nor sessions enter app storage.
type Auth struct {
	keyHash   [sha256.Size]byte
	sessions  *scs.SessionManager
	loginRate *rate.Limiter
	closeOnce sync.Once
}

func NewAuth(key string) (*Auth, error) {
	if len(strings.TrimSpace(key)) < 32 || len(key) > 4096 {
		return nil, errors.New("MYCDN_ADMIN_KEY must contain 32 to 4096 bytes; generate a strong random key")
	}
	sessions := scs.New()
	sessions.Lifetime = 12 * time.Hour
	sessions.HashTokenInStore = true
	sessions.Cookie.Name = sessionCookieName
	sessions.Cookie.Secure = false
	sessions.Cookie.SameSite = http.SameSiteStrictMode
	sessions.ErrorFunc = func(w http.ResponseWriter, _ *http.Request, _ error) {
		writeProblem(w, http.StatusInternalServerError, "management session failed")
	}
	return &Auth{keyHash: sha256.Sum256([]byte(key)), sessions: sessions, loginRate: rate.NewLimiter(rate.Every(5*time.Second), 10)}, nil
}

func (a *Auth) Close() {
	a.closeOnce.Do(func() { a.sessions.Store.(*memstore.MemStore).StopCleanup() })
}

func (a *Auth) protect(next http.Handler) http.Handler {
	return a.sessions.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		public := r.URL.Path == "/healthz" || r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/session"
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			public = public || r.URL.Path == "/" || r.URL.Path == "/favicon.svg" || r.URL.Path == "/licenses.txt" || strings.HasPrefix(r.URL.Path, "/assets/")
		}
		if !public && !a.sessions.GetBool(r.Context(), "authenticated") {
			writeProblem(w, http.StatusUnauthorized, "management login required")
			return
		}
		if r.URL.Path == "/api/auth/login" && r.Method == http.MethodPost {
			contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || contentType != "application/json" {
				writeProblem(w, http.StatusUnsupportedMediaType, "login requires application/json")
				return
			}
			if !a.loginRate.Allow() {
				w.Header().Set("Retry-After", "5")
				writeProblem(w, http.StatusTooManyRequests, "too many login attempts; try again shortly")
				return
			}
		}
		next.ServeHTTP(w, r)
	}))
}

type loginCredentials struct {
	Key string `json:"key" minLength:"32" maxLength:"4096" writeOnly:"true" doc:"Management key supplied through MYCDN_ADMIN_KEY, never stored in configuration."`
}

type sessionStatus struct {
	Authenticated bool `json:"authenticated"`
}

func (a *Auth) routes(api huma.API) {
	public := []map[string][]string{}
	huma.Register(api, huma.Operation{
		OperationID: "get-auth-session", Method: http.MethodGet, Path: "/api/auth/session",
		Summary: "Check the current management session without exposing credentials", Security: public,
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body sessionStatus }, error) {
		return &struct{ Body sessionStatus }{Body: sessionStatus{Authenticated: a.sessions.GetBool(ctx, "authenticated")}}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "login", Method: http.MethodPost, Path: "/api/auth/login",
		Summary: "Exchange the management key for a 12-hour, process-local session", Security: public,
		MaxBodyBytes: 8 << 10, BodyReadTimeout: 5 * time.Second, Errors: []int{400, 401, 403, 413, 415, 429},
	}, func(ctx context.Context, input *struct{ RawBody []byte }) (*struct{ Body sessionStatus }, error) {
		// Huma's generic body validation can echo submitted values in errors.
		// Decode this secret separately and return only fixed, redacted messages.
		var credentials loginCredentials
		if err := json.Unmarshal(input.RawBody, &credentials); err != nil || credentials.Key == "" || len(credentials.Key) > 4096 {
			return nil, huma.Error400BadRequest("provide a JSON object containing the management key")
		}
		hash := sha256.Sum256([]byte(credentials.Key))
		if subtle.ConstantTimeCompare(hash[:], a.keyHash[:]) != 1 {
			return nil, huma.Error401Unauthorized("invalid management key")
		}
		if err := a.sessions.RenewToken(ctx); err != nil {
			return nil, huma.Error500InternalServerError("management session failed")
		}
		a.sessions.Put(ctx, "authenticated", true)
		return &struct{ Body sessionStatus }{Body: sessionStatus{Authenticated: true}}, nil
	})
	api.OpenAPI().Paths["/api/auth/login"].Post.RequestBody.Content = map[string]*huma.MediaType{
		"application/json": {Schema: api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[loginCredentials](), true, "")},
	}
	huma.Register(api, huma.Operation{
		OperationID: "logout", Method: http.MethodPost, Path: "/api/auth/logout",
		Summary: "Revoke the current session and clear its cookie", Errors: []int{401, 403},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body sessionStatus }, error) {
		if err := a.sessions.Destroy(ctx); err != nil {
			return nil, huma.Error500InternalServerError("management session failed")
		}
		return &struct{ Body sessionStatus }{Body: sessionStatus{}}, nil
	})
}
