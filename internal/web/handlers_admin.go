package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dlouwers/typst-d2-mcp/internal/authdb"
)

// pageVM carries what every page's chrome needs: the title, who is
// signed in, and any flash banner. Embedded rather than repeated so the
// base template can rely on all four fields existing.
type pageVM struct {
	Title      string
	Admin      string
	Flash      string
	FlashLevel string
}

// usersVM is the view model for the user list.
type usersVM struct {
	pageVM
	Rows         []authdb.AdminUserRow
	DefaultQuota int
	PublicURL    string
}

// auditVM is the view model for the audit log.
type auditVM struct {
	pageVM
	Entries []authdb.AuditEntry
}

// utcDate is the day key compile counters are stored under.
func utcDate() string { return time.Now().UTC().Format("2006-01-02") }

func (s *Server) usersViewModel(r *http.Request) (usersVM, error) {
	rows, err := s.cfg.Store.AdminUsers(r.Context(), utcDate())
	if err != nil {
		return usersVM{}, err
	}
	return usersVM{
		pageVM: pageVM{
			Title: "Users — typst-d2-mcp admin",
			Admin: adminLogin(r.Context()),
		},
		Rows:         rows,
		DefaultQuota: s.cfg.QuotaDefault(),
		PublicURL:    strings.TrimRight(s.cfg.GitHub.Cfg.PublicURL, "/"),
	}, nil
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	vm, err := s.usersViewModel(r)
	if err != nil {
		slog.Error("admin: load users", "err", err)
		http.Error(w, "could not load users", http.StatusInternalServerError)
		return
	}
	// A plain form post lands back here with its message in the query.
	vm.Flash = r.URL.Query().Get("flash")
	vm.FlashLevel = r.URL.Query().Get("level")
	if vm.FlashLevel == "" {
		vm.FlashLevel = string(flashNotice)
	}
	s.renderPage(w, "users", vm)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.cfg.Store.ListAudit(r.Context(), 200)
	if err != nil {
		slog.Error("admin: load audit", "err", err)
		http.Error(w, "could not load audit log", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, "audit", auditVM{
		pageVM: pageVM{
			Title: "Audit — typst-d2-mcp admin",
			Admin: adminLogin(r.Context()),
		},
		Entries: entries,
	})
}

func (s *Server) renderPage(w http.ResponseWriter, name string, vm any) {
	tmpl, ok := s.pages[name]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Every page template defines "content"; "base" is the shell that
	// invokes it. Each page gets its own parsed set so the redefinition
	// of "content" does not collide.
	if err := tmpl.ExecuteTemplate(w, "base", vm); err != nil {
		slog.Error("admin: render page", "page", name, "err", err)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.cfg.Sessions.Clear(w)
	http.Redirect(w, r, "/admin/signin", http.StatusSeeOther)
}

// formLogin reads and normalises the target login from a form post.
func formLogin(r *http.Request) (string, error) {
	if err := r.ParseForm(); err != nil {
		return "", fmt.Errorf("could not read form")
	}
	login := authdb.NormalizeLogin(r.PostFormValue("login"))
	if login == "" {
		return "", fmt.Errorf("a GitHub login is required")
	}
	return login, nil
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	login, err := formLogin(r)
	if err != nil {
		s.respondAction(w, r, flashError, err.Error())
		return
	}
	if err := s.cfg.Store.CreateInvite(r.Context(), adminLogin(r.Context()), login); err != nil {
		slog.Error("admin: invite", "target", login, "err", err)
		s.respondAction(w, r, flashError, "could not invite "+login)
		return
	}
	slog.Info("admin: invited", "actor", adminLogin(r.Context()), "target", login)
	s.respondAction(w, r, flashNotice, fmt.Sprintf(
		"Invited %s. Send them %s — they sign in with GitHub.",
		login, strings.TrimRight(s.cfg.GitHub.Cfg.PublicURL, "/")))
}

// handleSetQuota accepts three modes so "unlimited" and "inherit the
// default" are explicit choices rather than magic numbers typed into a
// text field.
func (s *Server) handleSetQuota(w http.ResponseWriter, r *http.Request) {
	login, err := formLogin(r)
	if err != nil {
		s.respondAction(w, r, flashError, err.Error())
		return
	}
	var quota *int
	switch mode := r.PostFormValue("mode"); mode {
	case "default":
		quota = nil
	case "unlimited":
		zero := 0
		quota = &zero
	case "fixed":
		n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("value")))
		if err != nil || n < 1 {
			s.respondAction(w, r, flashError, "quota must be a whole number of compiles per day (1 or more)")
			return
		}
		quota = &n
	default:
		s.respondAction(w, r, flashError, "unknown quota mode "+mode)
		return
	}

	if err := s.cfg.Store.SetQuota(r.Context(), adminLogin(r.Context()), login, quota); err != nil {
		if errors.Is(err, authdb.ErrNoSuchUser) {
			s.respondAction(w, r, flashError,
				login+" has not signed in yet, so there is no account to set a quota on")
			return
		}
		slog.Error("admin: set quota", "target", login, "err", err)
		s.respondAction(w, r, flashError, "could not set quota for "+login)
		return
	}
	slog.Info("admin: quota set", "actor", adminLogin(r.Context()), "target", login)
	s.respondAction(w, r, flashNotice, "Quota updated for "+login)
}

func (s *Server) handleResetToday(w http.ResponseWriter, r *http.Request) {
	login, err := formLogin(r)
	if err != nil {
		s.respondAction(w, r, flashError, err.Error())
		return
	}
	userID := strings.TrimSpace(r.PostFormValue("user_id"))
	if userID == "" {
		s.respondAction(w, r, flashError, login+" has not signed in yet, so there is no counter to reset")
		return
	}
	if err := s.cfg.Store.ResetTodayCompiles(
		r.Context(), adminLogin(r.Context()), login, userID, utcDate()); err != nil {
		slog.Error("admin: reset today", "target", login, "err", err)
		s.respondAction(w, r, flashError, "could not reset today's counter for "+login)
		return
	}
	slog.Info("admin: reset today", "actor", adminLogin(r.Context()), "target", login)
	s.respondAction(w, r, flashNotice, "Today's counter reset for "+login)
}

func (s *Server) handleRevokeAccess(w http.ResponseWriter, r *http.Request) {
	login, err := formLogin(r)
	if err != nil {
		s.respondAction(w, r, flashError, err.Error())
		return
	}
	if err := s.cfg.Store.RevokeInvite(r.Context(), adminLogin(r.Context()), login); err != nil {
		slog.Error("admin: revoke access", "target", login, "err", err)
		s.respondAction(w, r, flashError, "could not revoke access for "+login)
		return
	}
	slog.Info("admin: access revoked", "actor", adminLogin(r.Context()), "target", login)
	// The allowlist is consulted on every request, so this is immediate
	// rather than pending — worth saying, since it is the reassuring
	// half of a scary action.
	s.respondAction(w, r, flashNotice, "Access revoked for "+login+" — effective immediately")
}

func (s *Server) handleRevokeKeys(w http.ResponseWriter, r *http.Request) {
	login, err := formLogin(r)
	if err != nil {
		s.respondAction(w, r, flashError, err.Error())
		return
	}
	n, err := s.cfg.Store.RevokeAPIKeys(r.Context(), adminLogin(r.Context()), login)
	if err != nil {
		slog.Error("admin: revoke keys", "target", login, "err", err)
		s.respondAction(w, r, flashError, "could not revoke keys for "+login)
		return
	}
	slog.Info("admin: keys revoked", "actor", adminLogin(r.Context()), "target", login, "count", n)
	s.respondAction(w, r, flashNotice, fmt.Sprintf(
		"Revoked %d key(s) for %s — they can sign in again for a fresh one", n, login))
}

// handleDeleteUser requires the admin to type the target login back,
// because there is no undo: the database rows and the workspace
// directory both go.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	login, err := formLogin(r)
	if err != nil {
		s.respondAction(w, r, flashError, err.Error())
		return
	}
	if authdb.NormalizeLogin(r.PostFormValue("confirm")) != login {
		s.respondAction(w, r, flashError,
			"type the login exactly to confirm deletion of "+login)
		return
	}

	userID, err := s.cfg.Store.DeleteUser(r.Context(), adminLogin(r.Context()), login)
	if err != nil {
		if errors.Is(err, authdb.ErrNoSuchUser) {
			s.respondAction(w, r, flashError, "no such user or invite: "+login)
			return
		}
		slog.Error("admin: delete user", "target", login, "err", err)
		s.respondAction(w, r, flashError, "could not delete "+login)
		return
	}

	// Database rows are gone; take the workspace with them. A failure
	// here is reported but not rolled back — the account is already
	// deleted, and leaving orphaned files is better than implying the
	// deletion did not happen. The sweeper does not collect orphaned
	// directories, so this is worth surfacing.
	if userID != "" && s.cfg.WorkspaceRoot != "" {
		if err := removeUserWorkspace(s.cfg.WorkspaceRoot, userID); err != nil {
			slog.Error("admin: delete workspace", "user", userID, "err", err)
			s.respondAction(w, r, flashError, fmt.Sprintf(
				"Deleted %s, but their workspace files could not be removed — see server logs", login))
			return
		}
	}
	slog.Info("admin: user deleted", "actor", adminLogin(r.Context()), "target", login, "user_id", userID)
	s.respondAction(w, r, flashNotice, "Deleted "+login+" and all their data")
}

// removeUserWorkspace deletes one tenant directory. userID comes from
// the database rather than the request, but it is still joined and
// re-checked against the root: a directory delete is not something to
// perform on an unverified path.
func removeUserWorkspace(root, userID string) error {
	if userID == "" || strings.ContainsAny(userID, `/\`) || userID == "." || userID == ".." {
		return fmt.Errorf("refusing to delete workspace for suspicious user id %q", userID)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	target := filepath.Clean(filepath.Join(absRoot, userID))
	rel, err := filepath.Rel(absRoot, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("workspace path %q escapes root", target)
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("remove workspace: %w", err)
	}
	return nil
}
