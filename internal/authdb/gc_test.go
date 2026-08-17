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
		wg              sync.WaitGroup
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
