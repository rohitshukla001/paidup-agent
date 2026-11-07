package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"paidup-agent/internal/domain"
)

const (
	cookieName = "paidup_session"
	cookieAge  = 7 * 24 * time.Hour
)

type ctxKey struct{}

// sessionID returns the session that requireSession put in the context.
func sessionID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// sign returns "<id>.<base64url HMAC-SHA256(secret, id)>".
func (s *server) sign(id string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte(id))
	return id + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verify returns the session ID of a valid cookie value, or "".
func (s *server) verify(value string) string {
	id, _, ok := strings.Cut(value, ".")
	if !ok || id == "" || s.cfg.SessionSecret == "" {
		return ""
	}
	if !hmac.Equal([]byte(s.sign(id)), []byte(value)) {
		return ""
	}
	return id
}

func (s *server) requireSession(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		id := ""
		if err == nil {
			id = s.verify(c.Value)
		}
		if id == "" {
			writeError(w, http.StatusUnauthorized, domain.CodeUnauthorized,
				"Log in first.")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DemoPassword == "" || s.cfg.SessionSecret == "" {
		s.log.Error("httpapi: DEMO_PASSWORD or SESSION_SECRET is not set")
		writeError(w, http.StatusServiceUnavailable, domain.CodeInternal,
			"Login is not set up on this server.")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, domain.CodeValidation,
			`Send {"password": "..."}.`)
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password),
		[]byte(s.cfg.DemoPassword)) != 1 {
		time.Sleep(300 * time.Millisecond) // slows down guessing
		writeError(w, http.StatusUnauthorized, domain.CodeUnauthorized,
			"Wrong password.")
		return
	}
	id, err := s.store.CreateSession(r.Context())
	if err != nil {
		s.writeStoreError(w, "create session", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    s.sign(id),
		Path:     "/",
		MaxAge:   int(cookieAge.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// session lets the web app check the cookie without other side effects.
func (s *server) session(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) demoReset(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AdminToken == "" {
		writeError(w, http.StatusNotFound, domain.CodeNotFound, "No such API route.")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Token")),
		[]byte(s.cfg.AdminToken)) != 1 {
		writeError(w, http.StatusUnauthorized, domain.CodeUnauthorized,
			"Wrong admin token.")
		return
	}
	if err := s.store.Reset(r.Context()); err != nil {
		s.writeStoreError(w, "reset", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
