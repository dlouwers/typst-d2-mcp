package authdb

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDeleteExpiredPDFLinks_OnlyExpired(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	// Both live now; the sweep runs with a clock two hours ahead, by
	// which point only the long-lived one survives.
	short, err := s.MintPDFLink(ctx, "gh:1", "short.pdf", time.Hour)
	if err != nil {
		t.Fatalf("mint short: %v", err)
	}
	long, err := s.MintPDFLink(ctx, "gh:1", "long.pdf", 24*time.Hour)
	if err != nil {
		t.Fatalf("mint long: %v", err)
	}

	n, err := s.DeleteExpiredPDFLinks(ctx, time.Now().UTC().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("DeleteExpiredPDFLinks: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted = %d, want 1", n)
	}

	if _, err := s.LookupPDFLink(ctx, short); !errors.Is(err, ErrPDFLinkNotFound) {
		t.Errorf("short link err = %v, want ErrPDFLinkNotFound", err)
	}
	if _, err := s.LookupPDFLink(ctx, long); err != nil {
		t.Errorf("long link should have survived: %v", err)
	}
}

func TestDeleteExpiredPDFLinks_EmptyTable(t *testing.T) {
	s := newStore(t)
	n, err := s.DeleteExpiredPDFLinks(t.Context(), time.Now().UTC())
	if err != nil {
		t.Fatalf("DeleteExpiredPDFLinks on empty table: %v", err)
	}
	if n != 0 {
		t.Errorf("deleted = %d, want 0", n)
	}
}

// The sweeper's DELETE and LookupPDFLink's opportunistic delete can race
// on the same expired row. Neither may error, and the row must end up
// gone either way.
func TestDeleteExpiredPDFLinks_RacesWithLookup(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	// 1ns TTL: expired by the time the insert has round-tripped.
	token, err := s.MintPDFLink(ctx, "gh:1", "out.pdf", time.Nanosecond)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	var (
		wg                sync.WaitGroup
		sweepErr, lookErr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, sweepErr = s.DeleteExpiredPDFLinks(ctx, time.Now().UTC())
	}()
	go func() {
		defer wg.Done()
		_, lookErr = s.LookupPDFLink(ctx, token)
	}()
	wg.Wait()

	if sweepErr != nil {
		t.Errorf("sweep errored during race: %v", sweepErr)
	}
	if lookErr != nil && !errors.Is(lookErr, ErrPDFLinkNotFound) {
		t.Errorf("lookup errored during race: %v", lookErr)
	}
	if _, err := s.LookupPDFLink(ctx, token); !errors.Is(err, ErrPDFLinkNotFound) {
		t.Errorf("expired row survived the race: %v", err)
	}
}

func TestLivePersistedPaths_GroupsAndCleans(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	// A minute ahead: past the 1ns link below, well short of the hour-long
	// ones. Reading the clock before minting would leave the 1ns link
	// still nominally live.
	now := time.Now().UTC().Add(time.Minute)

	if _, err := s.MintPDFLink(ctx, "gh:1", "./out.pdf", time.Hour); err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := s.MintPDFLink(ctx, "gh:1", "sub/deep.pdf", time.Hour); err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := s.MintPDFLink(ctx, "gh:2", "other.pdf", time.Hour); err != nil {
		t.Fatalf("mint: %v", err)
	}
	// Expired: must not appear in the live set.
	if _, err := s.MintPDFLink(ctx, "gh:1", "stale.pdf", time.Nanosecond); err != nil {
		t.Fatalf("mint: %v", err)
	}

	live, err := s.LivePersistedPaths(ctx, now)
	if err != nil {
		t.Fatalf("LivePersistedPaths: %v", err)
	}

	// "./out.pdf" must come back cleaned so it compares equal to a
	// walked path of "out.pdf".
	if !live["gh:1"]["out.pdf"] {
		t.Errorf("gh:1 live set missing cleaned out.pdf: %v", live["gh:1"])
	}
	if !live["gh:1"]["sub/deep.pdf"] {
		t.Errorf("gh:1 live set missing sub/deep.pdf: %v", live["gh:1"])
	}
	if live["gh:1"]["stale.pdf"] {
		t.Error("expired link appeared in the live set")
	}
	if !live["gh:2"]["other.pdf"] {
		t.Errorf("gh:2 live set wrong: %v", live["gh:2"])
	}
	if len(live["gh:1"]) != 2 {
		t.Errorf("gh:1 live set size = %d, want 2", len(live["gh:1"]))
	}
}

func TestWorkspaceUsage_UpsertAndRead(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	first := time.Now().UTC().Truncate(time.Second)

	if err := s.RecordWorkspaceUsage(ctx, "gh:1", 1024, first); err != nil {
		t.Fatalf("RecordWorkspaceUsage: %v", err)
	}
	usage, err := s.WorkspaceUsageByUser(ctx)
	if err != nil {
		t.Fatalf("WorkspaceUsageByUser: %v", err)
	}
	if got := usage["gh:1"].Bytes; got != 1024 {
		t.Errorf("bytes = %d, want 1024", got)
	}

	// Second pass overwrites rather than duplicating.
	second := first.Add(time.Hour)
	if err := s.RecordWorkspaceUsage(ctx, "gh:1", 2048, second); err != nil {
		t.Fatalf("second RecordWorkspaceUsage: %v", err)
	}
	usage, err = s.WorkspaceUsageByUser(ctx)
	if err != nil {
		t.Fatalf("WorkspaceUsageByUser: %v", err)
	}
	if len(usage) != 1 {
		t.Errorf("usage rows = %d, want 1 (upsert duplicated)", len(usage))
	}
	if got := usage["gh:1"].Bytes; got != 2048 {
		t.Errorf("bytes = %d, want 2048", got)
	}
	if !usage["gh:1"].ComputedAt.After(first) {
		t.Errorf("computed_at did not advance: %v not after %v",
			usage["gh:1"].ComputedAt, first)
	}
}

// An unmeasured user must be absent rather than zero, so the admin UI
// can tell "empty workspace" from "not yet swept".
func TestWorkspaceUsage_UnmeasuredUserAbsent(t *testing.T) {
	s := newStore(t)
	usage, err := s.WorkspaceUsageByUser(t.Context())
	if err != nil {
		t.Fatalf("WorkspaceUsageByUser: %v", err)
	}
	if _, ok := usage["gh:never"]; ok {
		t.Error("unmeasured user present in usage map")
	}
}

// registerAndUse registers a client and, when used is true, drives a
// full authorize → code → exchange so the client counts as in service.
func registerAndUse(t *testing.T, s *Store, name string, used bool) string {
	t.Helper()
	ctx := t.Context()
	c, err := s.RegisterClient(ctx, name, []string{"https://localhost/cb"}, "none")
	if err != nil {
		t.Fatalf("RegisterClient(%s): %v", name, err)
	}
	if !used {
		return c.ClientID
	}
	uid, err := s.UpsertGitHubUser(ctx, int64(len(name)+1), name+"-user", name+"@example.com")
	if err != nil {
		t.Fatalf("UpsertGitHubUser: %v", err)
	}
	code, err := s.MintAuthorizationCode(ctx, AuthorizationCode{
		UserDBID:            uid,
		ClientID:            c.ClientID,
		RedirectURI:         "https://localhost/cb",
		CodeChallenge:       "challenge",
		CodeChallengeMethod: "S256",
	})
	if err != nil {
		t.Fatalf("MintAuthorizationCode: %v", err)
	}
	if _, _, err := s.ConsumeAuthorizationCode(ctx, code); err != nil {
		t.Fatalf("ConsumeAuthorizationCode: %v", err)
	}
	return c.ClientID
}

func clientExists(t *testing.T, s *Store, clientID string) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM oauth_clients WHERE client_id = ?`, clientID).Scan(&n); err != nil {
		t.Fatalf("count clients: %v", err)
	}
	return n > 0
}

// The distinction the whole prune rests on: age alone must never be
// enough. A client that completed an exchange is a working integration,
// and sweeping it out from under someone who simply has not connected
// this month is the one outcome worse than the table growing.
func TestDeleteUnusedClients_NeverTouchesAClientThatWasUsed(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	used := registerAndUse(t, s, "in-service", true)
	unused := registerAndUse(t, s, "abandoned", false)

	// Far enough ahead that both are old by any measure.
	cutoff := time.Now().UTC().Add(24 * time.Hour)

	stale, err := s.UnusedClientsBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("UnusedClientsBefore: %v", err)
	}
	if len(stale) != 1 || stale[0].ClientID != unused {
		t.Fatalf("stale = %+v, want only the abandoned client", stale)
	}

	n, err := s.DeleteUnusedClients(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteUnusedClients: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted = %d, want 1", n)
	}
	if !clientExists(t, s, used) {
		t.Error("a client that completed an exchange was pruned")
	}
	if clientExists(t, s, unused) {
		t.Error("an abandoned client survived the prune")
	}
}

// Age is the other half of the rule: a registration made moments ago is
// mid-flow, not abandoned. The user in #142 produced roughly eight rows
// in a day, and the first of them was legitimate until it wasn't.
func TestDeleteUnusedClients_LeavesRecentRegistrations(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	fresh := registerAndUse(t, s, "just-registered", false)

	n, err := s.DeleteUnusedClients(ctx, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatalf("DeleteUnusedClients: %v", err)
	}
	if n != 0 {
		t.Errorf("deleted = %d, want 0", n)
	}
	if !clientExists(t, s, fresh) {
		t.Error("a registration from moments ago was pruned")
	}
}

// Deleting a client must take its sessions and codes with it. SQLite
// enforces ON DELETE CASCADE only when the foreign_keys pragma is on,
// and a pragma lives in a connection string where it is easy to lose,
// so this asserts the behaviour rather than the setting.
func TestDeleteUnusedClients_LeavesNoOrphans(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	c, err := s.RegisterClient(ctx, "abandoned", []string{"https://localhost/cb"}, "none")
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	// A session and a code that were minted and never redeemed — the
	// exact residue of an authorisation that was refused halfway.
	if _, err := s.CreateAuthorizeSession(ctx, AuthorizeSession{
		ClientID:            c.ClientID,
		RedirectURI:         "https://localhost/cb",
		CodeChallenge:       "challenge",
		CodeChallengeMethod: "S256",
		ClientState:         "cs",
		ExpiresAt:           time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateAuthorizeSession: %v", err)
	}
	uid, err := s.UpsertGitHubUser(ctx, 4242, "someone", "s@example.com")
	if err != nil {
		t.Fatalf("UpsertGitHubUser: %v", err)
	}
	if _, err := s.MintAuthorizationCode(ctx, AuthorizationCode{
		UserDBID:            uid,
		ClientID:            c.ClientID,
		RedirectURI:         "https://localhost/cb",
		CodeChallenge:       "challenge",
		CodeChallengeMethod: "S256",
	}); err != nil {
		t.Fatalf("MintAuthorizationCode: %v", err)
	}

	if _, err := s.DeleteUnusedClients(ctx, time.Now().UTC().Add(24*time.Hour)); err != nil {
		t.Fatalf("DeleteUnusedClients: %v", err)
	}

	for _, q := range []struct {
		what  string
		query string
	}{
		{"authorize sessions", `SELECT COUNT(*) FROM oauth_authorize_sessions WHERE client_id = ?`},
		{"authorization codes", `SELECT COUNT(*) FROM oauth_authorization_codes WHERE client_id = ?`},
	} {
		var n int
		if err := s.db.QueryRow(q.query, c.ClientID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", q.what, err)
		}
		if n != 0 {
			t.Errorf("%d orphaned %s survived the client", n, q.what)
		}
	}
}

// A client redeeming a code is what marks it in service, and it has to
// happen in the same transaction as the redemption — a token handed out
// while the registration still looks unused is a row the sweeper may
// delete under a working integration.
func TestConsumeAuthorizationCode_MarksTheClientUsed(t *testing.T) {
	s := newStore(t)
	id := registerAndUse(t, s, "exchanger", true)

	var lastUsed *time.Time
	if err := s.db.QueryRow(
		`SELECT last_used_at FROM oauth_clients WHERE client_id = ?`, id).Scan(&lastUsed); err != nil {
		t.Fatalf("read last_used_at: %v", err)
	}
	if lastUsed == nil {
		t.Fatal("a client that completed an exchange still reads as never used")
	}
}
