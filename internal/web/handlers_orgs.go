package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
)

// orgsVM is the view model for the organisations page.
type orgsVM struct {
	pageVM
	Orgs []orgView
}

// orgView is one organisation with its members expanded for display.
type orgView struct {
	authdb.Org
	Members []authdb.OrgMember
}

func (s *Server) orgsViewModel(r *http.Request) (orgsVM, error) {
	orgs, err := s.cfg.Store.ListOrgs(r.Context())
	if err != nil {
		return orgsVM{}, err
	}
	views := make([]orgView, 0, len(orgs))
	for _, o := range orgs {
		members, err := s.cfg.Store.ListOrgMembers(r.Context(), o.Slug)
		if err != nil {
			return orgsVM{}, err
		}
		views = append(views, orgView{Org: o, Members: members})
	}
	return orgsVM{
		pageVM: pageVM{
			Title: "Organisations — typst-d2-mcp admin",
			Admin: adminLogin(r.Context()),
			Build: s.cfg.Build,
		},
		Orgs: views,
	}, nil
}

func (s *Server) handleOrgs(w http.ResponseWriter, r *http.Request) {
	vm, err := s.orgsViewModel(r)
	if err != nil {
		slog.Error("admin: load orgs", "err", err)
		http.Error(w, "could not load organisations", http.StatusInternalServerError)
		return
	}
	vm.Flash = r.URL.Query().Get("flash")
	vm.FlashLevel = r.URL.Query().Get("level")
	if vm.FlashLevel == "" {
		vm.FlashLevel = string(flashNotice)
	}
	s.renderPage(w, "orgs", vm)
}

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.respondOrgAction(w, r, flashError, "could not read form")
		return
	}
	// Validate exactly what was typed (trim only, no lowercasing): the slug
	// becomes a permanent namespace in people's imports, so a malformed one
	// is an error to correct, not something to silently transform.
	slug := strings.TrimSpace(r.PostFormValue("slug"))
	name := strings.TrimSpace(r.PostFormValue("display_name"))
	if err := authdb.ValidateSlug(slug); err != nil {
		s.respondOrgAction(w, r, flashError, err.Error())
		return
	}
	if err := s.cfg.Store.CreateOrg(r.Context(), adminLogin(r.Context()), slug, name); err != nil {
		if errors.Is(err, authdb.ErrSlugTaken) {
			s.respondOrgAction(w, r, flashError, "an owner named "+slug+" already exists")
			return
		}
		if errors.Is(err, authdb.ErrInvalidSlug) {
			s.respondOrgAction(w, r, flashError, err.Error())
			return
		}
		slog.Error("admin: create org", "slug", slug, "err", err)
		s.respondOrgAction(w, r, flashError, "could not create organisation")
		return
	}
	slog.Info("admin: org created", "actor", adminLogin(r.Context()), "slug", slug)
	s.respondOrgAction(w, r, flashNotice, "Created organisation "+slug)
}

func (s *Server) handleDeleteOrg(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.respondOrgAction(w, r, flashError, "could not read form")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(r.PostFormValue("slug")))
	if err := s.cfg.Store.DeleteOrg(r.Context(), adminLogin(r.Context()), slug); err != nil {
		if errors.Is(err, authdb.ErrNoSuchOrg) {
			s.respondOrgAction(w, r, flashError, "no organisation named "+slug)
			return
		}
		slog.Error("admin: delete org", "slug", slug, "err", err)
		s.respondOrgAction(w, r, flashError, "could not delete organisation")
		return
	}
	slog.Info("admin: org deleted", "actor", adminLogin(r.Context()), "slug", slug)
	s.respondOrgAction(w, r, flashNotice, "Deleted organisation "+slug)
}

func (s *Server) handleAddOrgMember(w http.ResponseWriter, r *http.Request) {
	slug, login, err := orgMemberForm(r)
	if err != nil {
		s.respondOrgAction(w, r, flashError, err.Error())
		return
	}
	if err := s.cfg.Store.AddOrgMember(r.Context(), adminLogin(r.Context()), slug, login); err != nil {
		switch {
		case errors.Is(err, authdb.ErrNoSuchUser):
			s.respondOrgAction(w, r, flashError, login+" has not signed in yet, so there is no account to add")
		case errors.Is(err, authdb.ErrNoSuchOrg):
			s.respondOrgAction(w, r, flashError, "no organisation named "+slug)
		case errors.Is(err, authdb.ErrAlreadyMember):
			s.respondOrgAction(w, r, flashNotice, login+" is already a member of "+slug)
		default:
			slog.Error("admin: add org member", "slug", slug, "login", login, "err", err)
			s.respondOrgAction(w, r, flashError, "could not add "+login+" to "+slug)
		}
		return
	}
	slog.Info("admin: org member added", "actor", adminLogin(r.Context()), "slug", slug, "login", login)
	s.respondOrgAction(w, r, flashNotice, "Added "+login+" to "+slug)
}

func (s *Server) handleRemoveOrgMember(w http.ResponseWriter, r *http.Request) {
	slug, login, err := orgMemberForm(r)
	if err != nil {
		s.respondOrgAction(w, r, flashError, err.Error())
		return
	}
	if err := s.cfg.Store.RemoveOrgMember(r.Context(), adminLogin(r.Context()), slug, login); err != nil {
		if errors.Is(err, authdb.ErrNoSuchUser) {
			s.respondOrgAction(w, r, flashError, login+" is not a member of "+slug)
			return
		}
		slog.Error("admin: remove org member", "slug", slug, "login", login, "err", err)
		s.respondOrgAction(w, r, flashError, "could not remove "+login+" from "+slug)
		return
	}
	slog.Info("admin: org member removed", "actor", adminLogin(r.Context()), "slug", slug, "login", login)
	s.respondOrgAction(w, r, flashNotice, "Removed "+login+" from "+slug)
}

// orgMemberForm reads the (slug, login) pair the membership actions share.
func orgMemberForm(r *http.Request) (slug, login string, err error) {
	login, err = formLogin(r)
	if err != nil {
		return "", "", err
	}
	slug = strings.ToLower(strings.TrimSpace(r.PostFormValue("slug")))
	if slug == "" {
		return "", "", errors.New("an organisation is required")
	}
	return slug, login, nil
}

// respondOrgAction is the dual-mode reply for the org mutating handlers:
// an updated orgs list plus a flash for htmx, or a redirect carrying the
// message for a plain form post. Mirrors respondAction but targets the
// organisations page rather than the users page.
func (s *Server) respondOrgAction(w http.ResponseWriter, r *http.Request, level flashLevel, msg string) {
	if !isHTMX(r) {
		q := url.Values{}
		q.Set("flash", msg)
		q.Set("level", string(level))
		http.Redirect(w, r, "/admin/orgs?"+q.Encode(), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writeFlashOOB(w, level, msg)
	s.renderOrgsFragment(w, r)
}

func (s *Server) renderOrgsFragment(w http.ResponseWriter, r *http.Request) {
	vm, err := s.orgsViewModel(r)
	if err != nil {
		slog.Error("admin: build orgs fragment", "err", err)
		fmt.Fprint(w, `<sl-alert variant="error">could not load organisations</sl-alert>`)
		return
	}
	if err := s.fragments.ExecuteTemplate(w, "orgs-list", vm); err != nil {
		slog.Error("admin: render orgs fragment", "err", err)
	}
}
