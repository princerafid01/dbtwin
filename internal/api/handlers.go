package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/princerafid01/dbtwin/internal/branch"
	"github.com/princerafid01/dbtwin/internal/check"
	"github.com/princerafid01/dbtwin/internal/lock"
)

// handlers answers the /v1 routes, using svc for the branch work.
type handlers struct {
	svc BranchService
}

// branchJSON is how one branch looks in API answers.
type branchJSON struct {
	Name     string `json:"name"`                // e.g. "pr-42", or "main"
	Main     bool   `json:"main"`                // true for the main database
	Host     string `json:"host,omitempty"`      // e.g. 127.0.0.1, empty if not running
	Port     int    `json:"port,omitempty"`      // e.g. 41235, empty if not running
	Status   string `json:"status"`              // "running" or "not running"
	Used     string `json:"used,omitempty"`      // disk space, e.g. 1.2M
	DataFrom string `json:"data_from,omitempty"` // e.g. 2026-11-25T02:57:45+01:00, empty if unknown
}

// toJSON turns the branch package's Info into the API's shape.
func toJSON(b branch.Info) branchJSON {
	out := branchJSON{
		Name:   b.Name,
		Main:   b.Main,
		Host:   b.Host,
		Port:   b.Port,
		Status: b.Status,
		Used:   b.Used,
	}
	if !b.DataFrom.IsZero() {
		out.DataFrom = b.DataFrom.Format(time.RFC3339)
	}
	return out
}

// health answers GET /healthz.
func health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// list answers GET /v1/branches: main and every branch.
func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	infos, err := h.svc.List()
	if err != nil {
		failed(w, err)
		return
	}
	branches := []branchJSON{} // [] instead of null when there are none
	for _, b := range infos {
		branches = append(branches, toJSON(b))
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": branches})
}

// get answers GET /v1/branches/{name}: one branch.
func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	info, err := h.svc.Get(r.PathValue("name"))
	if err != nil {
		failed(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJSON(info))
}

// create answers POST /v1/branches with a body like {"name":"pr-42"}.
func (h handlers) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	// the body is only {"name":"..."}, so anything over 1 KB is a mistake or an attack;
	// reading past the limit fails and the client gets 400 bad_request
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // catch typos like {"nmae":"pr-42"}
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "body must be JSON like {\"name\":\"pr-42\"}: "+err.Error())
		return
	}

	info, err := h.svc.Create(body.Name)
	if err != nil {
		failed(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toJSON(info))
}

// reset answers POST /v1/branches/{name}/reset: main's newest data, same port.
func (h handlers) reset(w http.ResponseWriter, r *http.Request) {
	info, err := h.svc.Reset(r.PathValue("name"))
	if err != nil {
		failed(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJSON(info))
}

// delete answers DELETE /v1/branches/{name}.
func (h handlers) delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.svc.Delete(name); err != nil {
		failed(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "status": "deleted"})
}

// failed answers for an error from the branch code, with the HTTP status
// that matches its kind (see the Err* tags in the branch, lock and check packages).
// An unknown error is a 500 with only its message.
func failed(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, branch.ErrInvalidName):
		writeError(w, http.StatusBadRequest, "invalid_name", err.Error())
	case errors.Is(err, branch.ErrMainProtected):
		writeError(w, http.StatusBadRequest, "main_protected", err.Error())
	case errors.Is(err, branch.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, branch.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "already_exists", err.Error())
	case errors.Is(err, lock.ErrBusy):
		// tells the client how many seconds to wait before trying again
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusConflict, "busy", err.Error())
	case errors.Is(err, check.ErrNotReady):
		writeError(w, http.StatusServiceUnavailable, "not_ready", err.Error())
	default:
		// something we didn't expect: keep it in the server log too
		log.Printf("error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// writeError sends an error as {"error":{"code":"...","message":"..."}}.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

// writeJSON sends v as JSON with the given HTTP status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// the status is already sent, so all we can do is note it
		log.Printf("writing response failed: %v", err)
	}
}
