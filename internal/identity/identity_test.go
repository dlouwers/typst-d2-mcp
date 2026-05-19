package identity

import (
	"context"
	"testing"
)

func TestAnonymous(t *testing.T) {
	a := Anonymous()
	if !a.IsAnonymous() {
		t.Errorf("Anonymous() reports !IsAnonymous(): %+v", a)
	}
	if a.UserID == "" {
		t.Error("Anonymous().UserID is empty")
	}
}

func TestFromContext_DefaultsToAnonymous(t *testing.T) {
	id, ok := FromContext(context.Background())
	if ok {
		t.Errorf("FromContext on empty ctx should report ok=false, got true with %+v", id)
	}
	if !id.IsAnonymous() {
		t.Errorf("FromContext fallback is not anonymous: %+v", id)
	}
}

func TestWithIdentity_Roundtrip(t *testing.T) {
	want := Identity{UserID: "u_42", GitHubLogin: "octocat", Email: "octo@github.com"}
	ctx := WithIdentity(context.Background(), want)
	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext after WithIdentity reported ok=false")
	}
	if got != want {
		t.Errorf("FromContext returned %+v, want %+v", got, want)
	}
	if got.IsAnonymous() {
		t.Error("identity from WithIdentity should not be anonymous")
	}
}
