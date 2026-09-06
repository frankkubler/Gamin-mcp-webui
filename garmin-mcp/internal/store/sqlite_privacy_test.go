package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tamcore/garmin-mcp/internal/store"
)

// A digest-shaped value, and a second one that differs, standing for two notice
// texts. The tests never need the texts themselves: the store's contract is about
// the digest.
const (
	testNoticeHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testNoticeOther = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func TestAcceptPrivacyNoticeRecordsAndReadsBack(t *testing.T) {
	t.Parallel()
	opened, _ := newTestStore(t)
	ctx := t.Context()

	principal, err := opened.CreatePrincipal(ctx, testEmailNormalized)
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	_, found, err := opened.PrivacyNoticeAcceptance(ctx, principal.ID, testNoticeHash)
	if err != nil {
		t.Fatalf("PrivacyNoticeAcceptance: %v", err)
	}
	if found {
		t.Fatal("a principal who was never asked must not report an acceptance")
	}

	accepted, err := opened.AcceptPrivacyNotice(ctx, principal.ID, testNoticeHash, "2026-09-06")
	if err != nil {
		t.Fatalf("AcceptPrivacyNotice: %v", err)
	}
	if accepted.Version != "2026-09-06" || accepted.Hash != testNoticeHash {
		t.Errorf("recorded %+v, want the hash and version that were accepted", accepted)
	}
	if accepted.AcceptedAt.IsZero() {
		t.Error("an acceptance must carry the instant it happened")
	}

	read, found, err := opened.PrivacyNoticeAcceptance(ctx, principal.ID, testNoticeHash)
	if err != nil || !found {
		t.Fatalf("PrivacyNoticeAcceptance: %+v found = %v err = %v", read, found, err)
	}
	if !read.AcceptedAt.Equal(accepted.AcceptedAt) {
		t.Errorf("read back %v, want %v", read.AcceptedAt, accepted.AcceptedAt)
	}
}

func TestAcceptPrivacyNoticeKeepsTheFirstInstant(t *testing.T) {
	t.Parallel()
	opened, clock := newTestStore(t)
	ctx := t.Context()

	principal, err := opened.CreatePrincipal(ctx, testEmailNormalized)
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	first, err := opened.AcceptPrivacyNotice(ctx, principal.ID, testNoticeHash, "2026-09-06")
	if err != nil {
		t.Fatalf("AcceptPrivacyNotice: %v", err)
	}

	clock.advance(time.Hour)
	again, err := opened.AcceptPrivacyNotice(ctx, principal.ID, testNoticeHash, "2026-09-06")
	if err != nil {
		t.Fatalf("AcceptPrivacyNotice again: %v", err)
	}
	// The first acceptance is the one that happened; a later page view is not a
	// new consent and must not move the recorded instant.
	if !again.AcceptedAt.Equal(first.AcceptedAt) {
		t.Errorf("second acceptance moved the instant to %v, want %v",
			again.AcceptedAt, first.AcceptedAt)
	}
}

func TestAcceptPrivacyNoticeKeepsEveryVersionAccepted(t *testing.T) {
	t.Parallel()
	opened, clock := newTestStore(t)
	ctx := t.Context()

	principal, err := opened.CreatePrincipal(ctx, testEmailNormalized)
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	if _, err := opened.AcceptPrivacyNotice(
		ctx, principal.ID, testNoticeHash, "2026-01-01"); err != nil {
		t.Fatalf("AcceptPrivacyNotice: %v", err)
	}
	clock.advance(24 * time.Hour)
	if _, err := opened.AcceptPrivacyNotice(
		ctx, principal.ID, testNoticeOther, "2026-09-06"); err != nil {
		t.Fatalf("AcceptPrivacyNotice, second text: %v", err)
	}

	all, err := opened.PrivacyNoticeAcceptances(ctx, principal.ID)
	if err != nil {
		t.Fatalf("PrivacyNoticeAcceptances: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d acceptances, want both texts kept", len(all))
	}
	// Most recent first, so a report reads the current state at the top.
	if all[0].Version != "2026-09-06" || all[1].Version != "2026-01-01" {
		t.Errorf("order = %q then %q, want the most recent first",
			all[0].Version, all[1].Version)
	}
}

func TestAcceptPrivacyNoticeRefusesAnUnknownPrincipal(t *testing.T) {
	t.Parallel()
	opened, _ := newTestStore(t)

	_, err := opened.AcceptPrivacyNotice(
		t.Context(), "00000000-0000-4000-8000-000000000000", testNoticeHash, "2026-09-06")
	if !errors.Is(err, store.ErrPrincipalNotFound) {
		t.Errorf("err = %v, want ErrPrincipalNotFound", err)
	}
}

func TestAcceptPrivacyNoticeRefusesMalformedInput(t *testing.T) {
	t.Parallel()
	opened, _ := newTestStore(t)
	ctx := t.Context()

	principal, err := opened.CreatePrincipal(ctx, testEmailNormalized)
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	cases := map[string]struct{ hash, version string }{
		"short hash":        {"abc", "2026-09-06"},
		"uppercase hash":    {strings.ToUpper(testNoticeHash), "2026-09-06"},
		"non-hex hash":      {strings.Repeat("z", 64), "2026-09-06"},
		"empty version":     {testNoticeHash, ""},
		"control character": {testNoticeHash, "2026\n09-06"},
		"long version":      {testNoticeHash, strings.Repeat("v", 65)},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := opened.AcceptPrivacyNotice(
				ctx, principal.ID, input.hash, input.version); err == nil {
				t.Error("the store accepted a malformed acceptance")
			}
		})
	}
}

func TestUnlinkingGarminKeepsTheAcceptance(t *testing.T) {
	t.Parallel()
	opened, _ := newTestStore(t)
	ctx := t.Context()

	principal, err := opened.CreatePrincipal(ctx, testEmailNormalized)
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := opened.AcceptPrivacyNotice(
		ctx, principal.ID, testNoticeHash, "2026-09-06"); err != nil {
		t.Fatalf("AcceptPrivacyNotice: %v", err)
	}

	// Unlinking stops the processing the person consented to; it does not unsay
	// that they consented. The record is what proves which text they were shown,
	// so it outlives the linkage and goes only when the principal itself goes.
	if _, err := opened.UnlinkGarminAccount(ctx, principal.ID); err != nil {
		t.Fatalf("UnlinkGarminAccount: %v", err)
	}
	all, err := opened.PrivacyNoticeAcceptances(ctx, principal.ID)
	if err != nil {
		t.Fatalf("PrivacyNoticeAcceptances: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d acceptances after unlinking, want the record kept", len(all))
	}
}
