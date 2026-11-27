package api

import (
	"github.com/princerafid01/dbtwin/internal/branch"
	"github.com/princerafid01/dbtwin/internal/check"
	"github.com/princerafid01/dbtwin/internal/config"
	"github.com/princerafid01/dbtwin/internal/lock"
)

// BranchService is all the branch work the API needs.
// The routes only use this, never zfs or docker, so tests can pass
// a fake one and run without a real server.
type BranchService interface {
	List() ([]branch.Info, error)
	Get(name string) (branch.Info, error)
	Create(name string) (branch.Info, error)
	Reset(name string) (branch.Info, error)
	Delete(name string) error
}

// branchService is the real BranchService: it calls the branch package
// with dbtwin's settings, the same functions the CLI uses.
type branchService struct {
	cfg config.Config
}

// ready runs the startup checks again before each request.
// The server runs for days, and docker or zfs may have stopped since it started.
func (s branchService) ready() error {
	return check.All(s.cfg.MainDataset)
}

func (s branchService) List() ([]branch.Info, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return branch.List(s.cfg)
}

func (s branchService) Get(name string) (branch.Info, error) {
	if err := s.ready(); err != nil {
		return branch.Info{}, err
	}
	return branch.Get(s.cfg, name)
}

// Create, Reset and Delete change things, so each takes the same lock as
// the CLI for as long as it runs. If another change (from the API or the
// CLI) holds it, lock.Take fails right away with lock.ErrBusy: 409 busy.
// List and Get only read, so they don't lock.

func (s branchService) Create(name string) (branch.Info, error) {
	if err := s.ready(); err != nil {
		return branch.Info{}, err
	}
	release, err := lock.Take()
	if err != nil {
		return branch.Info{}, err
	}
	defer release()
	return branch.Create(s.cfg, name)
}

func (s branchService) Reset(name string) (branch.Info, error) {
	if err := s.ready(); err != nil {
		return branch.Info{}, err
	}
	release, err := lock.Take()
	if err != nil {
		return branch.Info{}, err
	}
	defer release()
	return branch.Reset(s.cfg, name)
}

func (s branchService) Delete(name string) error {
	if err := s.ready(); err != nil {
		return err
	}
	release, err := lock.Take()
	if err != nil {
		return err
	}
	defer release()
	return branch.Delete(s.cfg, name)
}
