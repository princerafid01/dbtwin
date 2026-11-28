// Package api is dbtwin's HTTP API ("dbtwin serve").
// CI systems use it to list, create, reset and delete branches.
package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/princerafid01/dbtwin/internal/config"
)

// Run starts the API with the real branch code and serves until it fails
// or is stopped. On a stop it lets running requests finish first.
func Run(cfg config.Config) error {
	// without a token anyone who can reach the port could delete branches
	if cfg.APIToken == "" {
		return errors.New(`config setting "api_token" is empty: "dbtwin serve" needs it (or set DBTWIN_API_TOKEN)`)
	}

	srv := &http.Server{
		Addr: cfg.APIAddress,
		// logRequests wraps every route, so each request gets one log line
		Handler: logRequests(New(cfg.APIToken, branchService{cfg: cfg})),

		// a client that sends its request very slowly is cut off,
		// so it can't hold a connection open forever
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// long enough for a create or reset (waiting for MySQL takes a while)
		WriteTimeout: 2 * time.Minute,
		// an unused keep-alive connection is closed after this
		IdleTimeout: 60 * time.Second,
	}

	// ctx ends on Ctrl+C (SIGINT) or "systemctl stop" (SIGTERM)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// with api_domain set, serve HTTPS ourselves (no Caddy needed)
	if cfg.APIDomain != "" {
		certs := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,                    // accept Let's Encrypt's terms
			HostPolicy: autocert.HostWhitelist(cfg.APIDomain), // only get certificates for our domain
			Cache:      autocert.DirCache(certDir),            // keep them across restarts
		}
		srv.Addr = ":443"
		// Let's Encrypt checks we own the domain over this same port 443,
		// so port 80 isn't needed
		srv.TLSConfig = certs.TLSConfig()
	}

	// serve in the background, so Run can wait for a stop signal at the same time
	served := make(chan error, 1)
	go func() {
		if srv.TLSConfig != nil {
			log.Printf("dbtwin API listening on https://%s (port 443)", cfg.APIDomain)
			served <- srv.ListenAndServeTLS("", "") // certificates come from TLSConfig
			return
		}
		log.Printf("dbtwin API listening on %s", cfg.APIAddress)
		served <- srv.ListenAndServe()
	}()

	select {
	case err := <-served:
		// the server never started, e.g. the port is already in use
		if srv.TLSConfig != nil {
			return fmt.Errorf("%w (if another web server like nginx or Caddy uses port 443, leave \"api_domain\" empty and put that server in front of dbtwin)", err)
		}
		return err
	case <-ctx.Done():
	}

	// from here a second Ctrl+C kills dbtwin right away, for when you can't wait
	stop()
	log.Printf("stopping: no new requests, waiting up to %s for running ones to finish", shutdownWait)

	// Shutdown closes the port, then waits until every running request
	// (e.g. a create half way through) has sent its answer
	wait, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	if err := srv.Shutdown(wait); err != nil {
		return fmt.Errorf("stopped before running requests finished: %w", err)
	}
	log.Printf("stopped")
	return nil
}

// shutdownWait is how long a stop waits for running requests.
// It matches WriteTimeout: no request can run longer than that anyway.
const shutdownWait = 2 * time.Minute

// certDir is where HTTPS certificates are saved when api_domain is set.
const certDir = "/var/lib/dbtwin/certs"

// New builds the API's routes.
// svc does the branch work; token is the secret every /v1 request must send.
func New(token string, svc BranchService) http.Handler {
	h := handlers{svc: svc}
	mux := http.NewServeMux()

	// no token needed: lets a load balancer or a person check the server is up
	mux.HandleFunc("GET /healthz", health)

	// {name} is read in the handler with r.PathValue("name")
	mux.Handle("GET /v1/branches", requireToken(token, h.list))
	mux.Handle("GET /v1/branches/{name}", requireToken(token, h.get))
	mux.Handle("POST /v1/branches", requireToken(token, h.create))
	mux.Handle("POST /v1/branches/{name}/reset", requireToken(token, h.reset))
	mux.Handle("DELETE /v1/branches/{name}", requireToken(token, h.delete))

	// any other /v1 path: still ask for the token first,
	// so someone without it learns nothing about which paths exist
	mux.Handle("/v1/", requireToken(token, func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such API route")
	}))

	return mux
}
