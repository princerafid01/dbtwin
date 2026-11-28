package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/princerafid01/dbtwin/internal/branch"
	"github.com/princerafid01/dbtwin/internal/check"
	"github.com/princerafid01/dbtwin/internal/lock"
)

// These tests use a fake BranchService, so they need no root, zfs or docker
// and run on any machine with "go test ./...".

const testToken = "test-token"

// fakeService answers every call with info/infos/err
// and remembers which call it got, and for which name.
type fakeService struct {
	info  branch.Info
	infos []branch.Info
	err   error

	called string // e.g. "create"
	name   string // the branch name it was called with
}

func (f *fakeService) List() ([]branch.Info, error) {
	f.called = "list"
	return f.infos, f.err
}

func (f *fakeService) Get(name string) (branch.Info, error) {
	f.called, f.name = "get", name
	return f.info, f.err
}

func (f *fakeService) Create(name string) (branch.Info, error) {
	f.called, f.name = "create", name
	return f.info, f.err
}

func (f *fakeService) Reset(name string) (branch.Info, error) {
	f.called, f.name = "reset", name
	return f.info, f.err
}

func (f *fakeService) Delete(name string) error {
	f.called, f.name = "delete", name
	return f.err
}

// send makes one request to the API (with the right token if auth is true)
// and returns the answer.
func send(t *testing.T, svc BranchService, method, path, body string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	rec := httptest.NewRecorder()
	New(testToken, svc).ServeHTTP(rec, req)
	return rec
}

// errorCode reads "code" from an answer like {"error":{"code":"...","message":"..."}}.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("answer is not JSON: %v\n%s", err, rec.Body)
	}
	return out.Error.Code
}

func TestHealthNeedsNoToken(t *testing.T) {
	rec := send(t, &fakeService{}, "GET", "/healthz", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Errorf("body = %s, want status ok", rec.Body)
	}
}

func TestTokenRequired(t *testing.T) {
	tests := []struct {
		name   string
		header string // the Authorization header sent; "" sends none
	}{
		{"no header", ""},
		{"wrong token", "Bearer wrong"},
		{"no Bearer word", testToken},
		{"empty token", "Bearer "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{}
			req := httptest.NewRequest("DELETE", "/v1/branches/pr-42", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			New(testToken, svc).ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if code := errorCode(t, rec); code != "unauthorized" {
				t.Errorf("code = %q, want unauthorized", code)
			}
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("WWW-Authenticate header missing")
			}
			if svc.called != "" {
				t.Errorf("service was called (%s) without a valid token", svc.called)
			}
		})
	}
}

func TestEmptyServerTokenRejectsAll(t *testing.T) {
	// New with an empty token must not let an empty Bearer through
	req := httptest.NewRequest("GET", "/v1/branches", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	New("", &fakeService{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestUnknownRoute(t *testing.T) {
	// without the token, an unknown path looks the same as a real one
	if rec := send(t, &fakeService{}, "GET", "/v1/nothing", "", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("without token: status = %d, want 401", rec.Code)
	}
	rec := send(t, &fakeService{}, "GET", "/v1/nothing", "", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("with token: status = %d, want 404", rec.Code)
	}
	if code := errorCode(t, rec); code != "not_found" {
		t.Errorf("code = %q, want not_found", code)
	}
}

func TestList(t *testing.T) {
	date := time.Date(2026, 11, 25, 2, 57, 45, 0, time.UTC)
	svc := &fakeService{infos: []branch.Info{
		{Name: "main", Main: true, Host: "127.0.0.1", Port: 3306, Status: branch.Running, Used: "11M", DataFrom: date},
		{Name: "pr-42", Status: branch.NotRunning},
	}}
	rec := send(t, svc, "GET", "/v1/branches", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out struct {
		Branches []branchJSON `json:"branches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := []branchJSON{
		{Name: "main", Main: true, Host: "127.0.0.1", Port: 3306, Status: "running", Used: "11M", DataFrom: "2026-11-25T02:57:45Z"},
		{Name: "pr-42", Status: "not running"},
	}
	if fmt.Sprint(out.Branches) != fmt.Sprint(want) {
		t.Errorf("branches =\n%+v\nwant\n%+v", out.Branches, want)
	}
}

func TestListEmptyIsArray(t *testing.T) {
	rec := send(t, &fakeService{}, "GET", "/v1/branches", "", true)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"branches":[]}` {
		t.Errorf("body = %s, want {\"branches\":[]}", got)
	}
}

func TestGet(t *testing.T) {
	svc := &fakeService{info: branch.Info{Name: "pr-42", Port: 41235, Status: branch.Running}}
	rec := send(t, svc, "GET", "/v1/branches/pr-42", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.called != "get" || svc.name != "pr-42" {
		t.Errorf("service got %s(%q), want get(\"pr-42\")", svc.called, svc.name)
	}
}

func TestCreate(t *testing.T) {
	svc := &fakeService{info: branch.Info{Name: "pr-42", Host: "127.0.0.1", Port: 41235, Status: branch.Running}}
	rec := send(t, svc, "POST", "/v1/branches", `{"name":"pr-42"}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\n%s", rec.Code, rec.Body)
	}
	if svc.called != "create" || svc.name != "pr-42" {
		t.Errorf("service got %s(%q), want create(\"pr-42\")", svc.called, svc.name)
	}
	var out branchJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "pr-42" || out.Port != 41235 || out.Status != "running" {
		t.Errorf("answer = %+v", out)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestCreateBadBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not JSON", `pr-42`},
		{"empty", ``},
		{"typo in key", `{"nmae":"pr-42"}`},
		{"over 1 KB", `{"name":"` + strings.Repeat("a", 2000) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{}
			rec := send(t, svc, "POST", "/v1/branches", tt.body, true)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if code := errorCode(t, rec); code != "bad_request" {
				t.Errorf("code = %q, want bad_request", code)
			}
			if svc.called != "" {
				t.Errorf("service was called (%s) with a bad body", svc.called)
			}
		})
	}
}

func TestReset(t *testing.T) {
	svc := &fakeService{info: branch.Info{Name: "pr-42", Port: 41235, Status: branch.Running}}
	rec := send(t, svc, "POST", "/v1/branches/pr-42/reset", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.called != "reset" || svc.name != "pr-42" {
		t.Errorf("service got %s(%q), want reset(\"pr-42\")", svc.called, svc.name)
	}
}

func TestDelete(t *testing.T) {
	svc := &fakeService{}
	rec := send(t, svc, "DELETE", "/v1/branches/pr-42", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"name":"pr-42","status":"deleted"}` {
		t.Errorf("body = %s", got)
	}
	if svc.called != "delete" || svc.name != "pr-42" {
		t.Errorf("service got %s(%q), want delete(\"pr-42\")", svc.called, svc.name)
	}
}

func TestErrorCodes(t *testing.T) {
	// wrapped like the real code does it, so errors.Is must look inside
	wrap := func(err error) error { return fmt.Errorf("branch pr-42: %w", err) }

	tests := []struct {
		err    error
		status int
		code   string
	}{
		{wrap(branch.ErrInvalidName), 400, "invalid_name"},
		{wrap(branch.ErrMainProtected), 400, "main_protected"},
		{wrap(branch.ErrNotFound), 404, "not_found"},
		{wrap(branch.ErrAlreadyExists), 409, "already_exists"},
		{wrap(lock.ErrBusy), 409, "busy"},
		{wrap(check.ErrNotReady), 503, "not_ready"},
		{errors.New("zfs exploded"), 500, "internal"},
	}

	// a 500 logs the error; keep the test output clean
	log.SetOutput(&bytes.Buffer{})
	defer log.SetOutput(os.Stderr)

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			// the same error must give the same answer on every route
			routes := []struct{ method, path, body string }{
				{"GET", "/v1/branches", ""},
				{"GET", "/v1/branches/pr-42", ""},
				{"POST", "/v1/branches", `{"name":"pr-42"}`},
				{"POST", "/v1/branches/pr-42/reset", ""},
				{"DELETE", "/v1/branches/pr-42", ""},
			}
			for _, r := range routes {
				rec := send(t, &fakeService{err: tt.err}, r.method, r.path, r.body, true)
				if rec.Code != tt.status {
					t.Errorf("%s %s: status = %d, want %d", r.method, r.path, rec.Code, tt.status)
				}
				if code := errorCode(t, rec); code != tt.code {
					t.Errorf("%s %s: code = %q, want %q", r.method, r.path, code, tt.code)
				}
				retry := rec.Header().Get("Retry-After")
				if tt.code == "busy" && retry != "10" {
					t.Errorf("%s %s: Retry-After = %q, want 10", r.method, r.path, retry)
				}
				if tt.code != "busy" && retry != "" {
					t.Errorf("%s %s: Retry-After = %q, want none", r.method, r.path, retry)
				}
			}
		})
	}
}

func TestLogLine(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	req := httptest.NewRequest("DELETE", "/v1/branches/pr-42?secret=x", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	logRequests(New(testToken, &fakeService{err: branch.ErrNotFound})).ServeHTTP(httptest.NewRecorder(), req)

	line := buf.String()
	if !strings.Contains(line, "DELETE /v1/branches/pr-42 404 ") {
		t.Errorf("log line = %q, want method, path and status", line)
	}
	if strings.Contains(line, testToken) || strings.Contains(line, "secret") {
		t.Errorf("log line = %q, must not hold the token or the query", line)
	}
	if strings.Count(line, "\n") != 1 {
		t.Errorf("got %d log lines, want 1:\n%s", strings.Count(line, "\n"), line)
	}
}
