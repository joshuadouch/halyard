package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/joshuadouch/halyard/internal/auth"
	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/store"
)

// accessListView is an access list as the UI sees it: password hashes are
// never sent, and each list says which hosts use it.
type accessListView struct {
	model.AccessList
	Hosts []string `json:"hosts"`
}

func redact(a model.AccessList) model.AccessList {
	users := make([]model.AuthUser, 0, len(a.Users))
	for _, u := range a.Users {
		users = append(users, model.AuthUser{Username: u.Username})
	}
	a.Users = users
	if a.AllowCIDRs == nil {
		a.AllowCIDRs = []string{}
	}
	return a
}

func (s *Server) listAccessLists(w http.ResponseWriter, r *http.Request) {
	lists, err := s.Store.AccessLists(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	hosts, _ := s.Store.Hosts(r.Context())
	out := []accessListView{}
	for _, a := range lists {
		v := accessListView{AccessList: redact(a), Hosts: []string{}}
		for _, h := range hosts {
			if h.AccessListID == a.ID {
				v.Hosts = append(v.Hosts, first(h.Hostnames))
			}
		}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

func (s *Server) saveAccessList(w http.ResponseWriter, r *http.Request) {
	var a model.AccessList
	if err := readJSON(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := r.PathValue("id")
	ok := s.mutate(w, r, "Saved access list "+a.Name, false, func(ctx context.Context) error {
		old := map[string]string{}
		if id != "" {
			var prev model.AccessList
			if err := s.Store.Get(ctx, store.KindACL, id, &prev); err != nil {
				return err
			}
			for _, u := range prev.Users {
				old[u.Username] = u.PasswordHash
			}
			a.ID = id
		} else {
			a.ID = store.NewID()
		}
		// A blank password keeps the user's current one.
		for i := range a.Users {
			u := &a.Users[i]
			if u.Password != "" {
				u.PasswordHash = auth.HashCrypt(u.Password)
			} else {
				u.PasswordHash = old[u.Username]
			}
			u.Password = ""
		}
		if err := a.Normalize(); err != nil {
			return err
		}
		all, err := s.Store.AccessLists(ctx)
		if err != nil {
			return err
		}
		for _, o := range all {
			if o.ID != a.ID && o.Name == a.Name {
				return fmt.Errorf("an access list called %s already exists", a.Name)
			}
		}
		return s.Store.Put(ctx, store.KindACL, a.ID, a)
	})
	if ok {
		writeJSON(w, 200, redact(a))
	}
}

func (s *Server) deleteAccessList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ok := s.mutate(w, r, "Deleted access list", false, func(ctx context.Context) error {
		hosts, err := s.Store.Hosts(ctx)
		if err != nil {
			return err
		}
		for _, h := range hosts {
			if h.AccessListID == id {
				return fmt.Errorf("host %s still uses this access list", first(h.Hostnames))
			}
		}
		return s.Store.Delete(ctx, store.KindACL, id)
	})
	if ok {
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

// checkHostAccess validates a host's access list reference.
func (s *Server) checkHostAccess(ctx context.Context, h model.Host) error {
	if h.AccessListID == "" {
		return nil
	}
	var a model.AccessList
	if err := s.Store.Get(ctx, store.KindACL, h.AccessListID, &a); err != nil {
		return errors.New("the selected access list does not exist")
	}
	if h.Mode == model.ModeTCP && len(a.Users) > 0 {
		return fmt.Errorf("TCP hosts cannot ask for a password; pick an access list without users")
	}
	return nil
}
