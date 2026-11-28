package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireToken lets a request through to next only if it sends
// "Authorization: Bearer <token>". Anything else gets 401 and next never runs.
// The token is never logged or put in an answer.
func requireToken(token string, next http.HandlerFunc) http.Handler {
	want := []byte(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// e.g. "Bearer s3cret" -> "s3cret"; ok is false if "Bearer " is missing
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

		// ConstantTimeCompare takes the same time however many characters match,
		// so nobody can guess the token letter by letter by timing our answers.
		// an empty token never matches, even if the request sends an empty one.
		if !ok || token == "" || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			// tells clients which kind of login we expect
			w.Header().Set("WWW-Authenticate", `Bearer realm="dbtwin"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", `missing or wrong API token (send "Authorization: Bearer <token>")`)
			return
		}

		next(w, r)
	})
}
