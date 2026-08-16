package authdb

import (
	"errors"
	"testing"
	"time"
)

const today = "2026-08-16"

// seedUser creates a signed-in user and returns their identity key.
func seedUser(t *testing.T, s *Store, githubID int64, login string) string {
	t.Helper()
	if _, err := s.UpsertGitHubUser(t.Context(), githubID, login, login+"@example.com"); err != nil {
		t.Fatalf("UpsertGitHubUser: %v", err)
	}
	return "gh:" + itoa(githubID)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestInvites_CreateCheckRevoke(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	if ok, _ := s.IsInvited(ctx, "newbie"); ok {
		t.Error("uninvited login reported as invited")
	}
	if err := s.CreateInvite(ctx, "dlouwers", "Newbie"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	// Logins are case-insensitive on GitHub, so the check must be too.
	if ok, err := s.IsInvited(ctx, "newbie"); err != nil || !ok {
		t.Errorf("IsInvited(newbie) = %v, %v; want true", ok, err)
	}
	if ok, _ := s.IsInvited(ctx, "NEWBIE"); !ok {
		t.Error("invite lookup is case-sensitive")
	}

	if err := s.RevokeInvite(ctx, "dlouwers", "newbie"); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if ok, _ := s.IsInvited(ctx, "newbie"); ok {
		t.Error("invite survived revocation")
	}
}

// A double-clicked invite button must not be an error.
func TestInvites_CreateIsIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	if err := s.CreateInvite(ctx, "dlouwers", "newbie"); err != nil {
		t.Fatalf("first CreateInvite: %v", err)
	}
	if err := s.CreateInvite(ctx, "someoneelse", "newbie"); err != nil {
		t.Fatalf("second CreateInvite: %v", err)
	}

	rows, err := s.AdminUsers(ctx, today)
	if err != nil {
		t.Fatalf("AdminUsers: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (duplicate invite created a second row)", len(rows))
	}
	if rows[0].InvitedBy != "dlouwers" {
		t.Errorf("InvitedBy = %q, want the original inviter", rows[0].InvitedBy)
	}
}

func TestQuota_SetUnlimitedAndDefault(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	userID := seedUser(t, s, 42, "octocat")

	// No override: inherits whatever default is passed in.
	if got, err := s.EffectiveQuota(ctx, userID, 5); err != nil || got != 5 {
		t.Errorf("EffectiveQuota with no override = %d, %v; want 5", got, err)
	}

	ten := 10
	if err := s.SetQuota(ctx, "dlouwers", "octocat", &ten); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	if got, _ := s.EffectiveQuota(ctx, userID, 5); got != 10 {
		t.Errorf("EffectiveQuota after override = %d, want 10", got)
	}

	// 0 is unlimited, and must survive the round trip rather than being
	// mistaken for "unset".
	zero := 0
	if err := s.SetQuota(ctx, "dlouwers", "octocat", &zero); err != nil {
		t.Fatalf("SetQuota unlimited: %v", err)
	}
	if got, _ := s.EffectiveQuota(ctx, userID, 5); got != 0 {
		t.Errorf("EffectiveQuota unlimited = %d, want 0", got)
	}

	// nil clears the override.
	if err := s.SetQuota(ctx, "dlouwers", "octocat", nil); err != nil {
		t.Fatalf("SetQuota default: %v", err)
	}
	if got, _ := s.EffectiveQuota(ctx, userID, 5); got != 5 {
		t.Errorf("EffectiveQuota after clearing = %d, want 5", got)
	}
}

func TestQuota_SetOnUnknownUser(t *testing.T) {
	s := newStore(t)
	five := 5
	err := s.SetQuota(t.Context(), "dlouwers", "ghost", &five)
	if !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("err = %v, want ErrNoSuchUser", err)
	}
}

// An unknown user must fall back to the default rather than erroring, so
// a compile racing a deletion fails closed.
func TestQuota_UnknownUserFallsBackToDefault(t *testing.T) {
	s := newStore(t)
	got, err := s.EffectiveQuota(t.Context(), "gh:999", 3)
	if err != nil {
		t.Fatalf("EffectiveQuota: %v", err)
	}
	if got != 3 {
		t.Errorf("quota = %d, want the default 3", got)
	}
}

func TestResetTodayCompiles(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	userID := seedUser(t, s, 42, "octocat")

	if err := s.IncrementCompile(ctx, userID, today, 1); err != nil {
		t.Fatalf("IncrementCompile: %v", err)
	}
	if err := s.IncrementCompile(ctx, userID, today, 1); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected quota exceeded, got %v", err)
	}

	if err := s.ResetTodayCompiles(ctx, "dlouwers", "octocat", userID, today); err != nil {
		t.Fatalf("ResetTodayCompiles: %v", err)
	}
	if err := s.IncrementCompile(ctx, userID, today, 1); err != nil {
		t.Errorf("compile after reset: %v", err)
	}
}

// Resetting one user/day must not disturb another.
func TestResetTodayCompiles_ScopedToUserAndDay(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	a := seedUser(t, s, 1, "alice")
	b := seedUser(t, s, 2, "bob")

	for _, u := range []string{a, b} {
		if err := s.IncrementCompile(ctx, u, today, 5); err != nil {
			t.Fatalf("increment: %v", err)
		}
	}
	if err := s.IncrementCompile(ctx, a, "2026-08-15", 5); err != nil {
		t.Fatalf("increment yesterday: %v", err)
	}

	if err := s.ResetTodayCompiles(ctx, "dlouwers", "alice", a, today); err != nil {
		t.Fatalf("ResetTodayCompiles: %v", err)
	}

	rows, err := s.AdminUsers(ctx, today)
	if err != nil {
		t.Fatalf("AdminUsers: %v", err)
	}
	byLogin := map[string]AdminUserRow{}
	for _, r := range rows {
		byLogin[r.GitHubLogin] = r
	}
	if got := byLogin["alice"].UsedToday; got != 0 {
		t.Errorf("alice used today = %d, want 0", got)
	}
	if got := byLogin["bob"].UsedToday; got != 1 {
		t.Errorf("bob used today = %d, want 1 (other user disturbed)", got)
	}
	// Yesterday's row for alice must survive.
	var yesterday int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count FROM compiles WHERE user_id = ? AND utc_date = ?`,
		a, "2026-08-15").Scan(&yesterday); err != nil {
		t.Fatalf("yesterday row lost: %v", err)
	}
}

func TestRevokeAPIKeys(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	uid, err := s.UpsertGitHubUser(ctx, 42, "octocat", "")
	if err != nil {
		t.Fatalf("UpsertGitHubUser: %v", err)
	}
	key, err := s.IssueAPIKey(ctx, uid)
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}

	n, err := s.RevokeAPIKeys(ctx, "dlouwers", "octocat")
	if err != nil {
		t.Fatalf("RevokeAPIKeys: %v", err)
	}
	if n != 1 {
		t.Errorf("revoked = %d, want 1", n)
	}
	if _, err := s.IdentityForKey(ctx, key); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("revoked key still authenticates: %v", err)
	}
	// The user itself survives — they can sign in again for a fresh key.
	rows, _ := s.AdminUsers(ctx, today)
	if len(rows) != 1 || !rows[0].SignedIn {
		t.Errorf("user row lost during key revocation: %+v", rows)
	}
}

func TestDeleteUser_RemovesEverything(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	uid, err := s.UpsertGitHubUser(ctx, 42, "octocat", "")
	if err != nil {
		t.Fatalf("UpsertGitHubUser: %v", err)
	}
	userID := "gh:42"
	if _, err := s.IssueAPIKey(ctx, uid); err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	if err := s.CreateInvite(ctx, "dlouwers", "octocat"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	token, err := s.MintPDFLink(ctx, userID, "out.pdf", 3600*1e9)
	if err != nil {
		t.Fatalf("MintPDFLink: %v", err)
	}
	if err := s.IncrementCompile(ctx, userID, today, 5); err != nil {
		t.Fatalf("IncrementCompile: %v", err)
	}
	if err := s.RecordWorkspaceUsage(ctx, userID, 1234, nowUTC()); err != nil {
		t.Fatalf("RecordWorkspaceUsage: %v", err)
	}

	gotID, err := s.DeleteUser(ctx, "dlouwers", "octocat")
	if err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if gotID != userID {
		t.Errorf("returned user id = %q, want %q", gotID, userID)
	}

	// Nothing left anywhere.
	rows, _ := s.AdminUsers(ctx, today)
	if len(rows) != 0 {
		t.Errorf("rows after delete = %d, want 0: %+v", len(rows), rows)
	}
	if _, err := s.LookupPDFLink(ctx, token); !errors.Is(err, ErrPDFLinkNotFound) {
		t.Error("deleted user's download link still resolves")
	}
	usage, _ := s.WorkspaceUsageByUser(ctx)
	if _, ok := usage[userID]; ok {
		t.Error("workspace usage row survived delete")
	}
	var keys, compiles int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys`).Scan(&keys)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM compiles WHERE user_id = ?`, userID).Scan(&compiles)
	if keys != 0 {
		t.Errorf("api_keys rows = %d, want 0", keys)
	}
	if compiles != 0 {
		t.Errorf("compiles rows = %d, want 0", compiles)
	}
}

// Deleting someone who only ever had an invite must work, not error.
func TestDeleteUser_InviteOnly(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	if err := s.CreateInvite(ctx, "dlouwers", "newbie"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	userID, err := s.DeleteUser(ctx, "dlouwers", "newbie")
	if err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if userID != "" {
		t.Errorf("user id = %q, want empty for an invite-only delete", userID)
	}
	if ok, _ := s.IsInvited(ctx, "newbie"); ok {
		t.Error("invite survived delete")
	}
}

func TestDeleteUser_UnknownLogin(t *testing.T) {
	s := newStore(t)
	if _, err := s.DeleteUser(t.Context(), "dlouwers", "ghost"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("err = %v, want ErrNoSuchUser", err)
	}
}

func TestAdminUsers_MergesUsersAndInvites(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	seedUser(t, s, 1, "zoe")   // signed in, not invited (env allowlist era)
	seedUser(t, s, 2, "alice") // signed in and invited
	if err := s.CreateInvite(ctx, "dlouwers", "alice"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := s.CreateInvite(ctx, "dlouwers", "bob"); err != nil { // invited only
		t.Fatalf("CreateInvite: %v", err)
	}

	rows, err := s.AdminUsers(ctx, today)
	if err != nil {
		t.Fatalf("AdminUsers: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(rows), rows)
	}
	// Sorted by login: alice, bob, zoe.
	if rows[0].GitHubLogin != "alice" || rows[1].GitHubLogin != "bob" || rows[2].GitHubLogin != "zoe" {
		t.Errorf("rows not sorted by login: %v %v %v",
			rows[0].GitHubLogin, rows[1].GitHubLogin, rows[2].GitHubLogin)
	}
	if !rows[0].SignedIn || !rows[0].Invited {
		t.Errorf("alice should be both signed in and invited: %+v", rows[0])
	}
	if rows[1].SignedIn || !rows[1].Invited {
		t.Errorf("bob should be invited but not signed in: %+v", rows[1])
	}
	if rows[1].UserID != "" {
		t.Errorf("pending invite has a user id %q, want empty", rows[1].UserID)
	}
	if !rows[2].SignedIn || rows[2].Invited {
		t.Errorf("zoe should be signed in but not invited: %+v", rows[2])
	}
}

func TestAdminUsers_ReportsUsageKeysAndStorage(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	uid, _ := s.UpsertGitHubUser(ctx, 42, "octocat", "")
	if _, err := s.IssueAPIKey(ctx, uid); err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	if err := s.IncrementCompile(ctx, "gh:42", today, 10); err != nil {
		t.Fatalf("IncrementCompile: %v", err)
	}
	if err := s.RecordWorkspaceUsage(ctx, "gh:42", 4096, nowUTC()); err != nil {
		t.Fatalf("RecordWorkspaceUsage: %v", err)
	}

	rows, err := s.AdminUsers(ctx, today)
	if err != nil {
		t.Fatalf("AdminUsers: %v", err)
	}
	r := rows[0]
	if r.UsedToday != 1 {
		t.Errorf("UsedToday = %d, want 1", r.UsedToday)
	}
	if r.KeyCount != 1 {
		t.Errorf("KeyCount = %d, want 1", r.KeyCount)
	}
	if r.StorageBytes == nil || *r.StorageBytes != 4096 {
		t.Errorf("StorageBytes = %v, want 4096", r.StorageBytes)
	}
	if r.StorageMeasAt == nil {
		t.Error("StorageMeasAt is nil despite a recorded measurement")
	}
}

// An unmeasured user's storage stays nil so the UI can say "not yet
// computed" rather than claiming zero bytes.
func TestAdminUsers_UnmeasuredStorageIsNil(t *testing.T) {
	s := newStore(t)
	seedUser(t, s, 42, "octocat")

	rows, err := s.AdminUsers(t.Context(), today)
	if err != nil {
		t.Fatalf("AdminUsers: %v", err)
	}
	if rows[0].StorageBytes != nil {
		t.Errorf("StorageBytes = %v, want nil", *rows[0].StorageBytes)
	}
}

func TestAudit_RecordsEveryAction(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	seedUser(t, s, 42, "octocat")

	five := 5
	if err := s.CreateInvite(ctx, "dlouwers", "octocat"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := s.SetQuota(ctx, "dlouwers", "octocat", &five); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	if err := s.ResetTodayCompiles(ctx, "dlouwers", "octocat", "gh:42", today); err != nil {
		t.Fatalf("ResetTodayCompiles: %v", err)
	}
	if _, err := s.RevokeAPIKeys(ctx, "dlouwers", "octocat"); err != nil {
		t.Fatalf("RevokeAPIKeys: %v", err)
	}
	if err := s.RevokeInvite(ctx, "dlouwers", "octocat"); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if _, err := s.DeleteUser(ctx, "dlouwers", "octocat"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	entries, err := s.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 6 {
		t.Fatalf("audit entries = %d, want 6: %+v", len(entries), entries)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Action] = true
		if e.ActorLogin != "dlouwers" {
			t.Errorf("actor = %q, want dlouwers", e.ActorLogin)
		}
		if e.TargetLogin != "octocat" {
			t.Errorf("target = %q, want octocat", e.TargetLogin)
		}
	}
	for _, want := range []string{
		ActionInvite, ActionSetQuota, ActionResetQuota,
		ActionRevokeKeys, ActionRevoke, ActionDeleteUser,
	} {
		if !seen[want] {
			t.Errorf("no audit entry for action %q", want)
		}
	}
}

// A failed action must leave no audit row: the record and the change
// share a transaction.
func TestAudit_NoRowWhenActionFails(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	five := 5
	if err := s.SetQuota(ctx, "dlouwers", "ghost", &five); !errors.Is(err, ErrNoSuchUser) {
		t.Fatalf("SetQuota on unknown user = %v, want ErrNoSuchUser", err)
	}
	entries, err := s.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("audit entries = %d, want 0 after a failed action: %+v", len(entries), entries)
	}
}

// nowUTC is a tiny helper so tests don't import time solely for this.
func nowUTC() time.Time { return time.Now().UTC() }
