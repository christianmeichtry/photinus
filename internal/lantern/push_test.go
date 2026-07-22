package lantern

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/christianmeichtry/photinus/internal/notify"
)

func TestPushRegistrationGossips(t *testing.T) {
	now := time.Now().UTC()
	a := New(Config{ID: "a"})
	b := New(Config{ID: "b"})

	a.RegisterPush("aa11", "production", now)

	// The registration rides a's flash envelope and lands in b.
	payload := a.pushPayload()
	if payload == nil {
		t.Fatal("a registration must ride the flash")
	}
	b.ReceiveFlash(payload)
	regs := b.PushRegistrations()
	if len(regs) != 1 || regs[0].Token != "aa11" || regs[0].Env != "production" {
		t.Fatalf("b did not learn the registration: %+v", regs)
	}

	// It also rides the anti-entropy sync, so late joiners hear it.
	c := New(Config{ID: "c"})
	c.ReceiveFlash(a.SyncState())
	if got := c.PushRegistrations(); len(got) != 1 || got[0].Token != "aa11" {
		t.Fatalf("sync state did not carry the registration: %+v", got)
	}
}

func TestPushRegistrationNewestWins(t *testing.T) {
	now := time.Now().UTC()
	l := New(Config{ID: "a"})
	l.RegisterPush("aa11", "sandbox", now)

	// An older word about the same token, heard late, does not regress it.
	stale, _ := json.Marshal(envelope{V: flashV, Push: []notify.PushRegistration{
		{Token: "aa11", Env: "production", Seen: now.Add(-time.Hour)},
	}})
	l.ReceiveFlash(stale)
	if got := l.PushRegistrations(); got[0].Env != "sandbox" {
		t.Fatalf("an older registration overwrote a newer one: %+v", got)
	}

	// A newer word does.
	fresh, _ := json.Marshal(envelope{V: flashV, Push: []notify.PushRegistration{
		{Token: "aa11", Env: "production", Seen: now.Add(time.Hour)},
	}})
	l.ReceiveFlash(fresh)
	if got := l.PushRegistrations(); got[0].Env != "production" {
		t.Fatalf("a newer registration was ignored: %+v", got)
	}
}

func TestPushRegistrationAgesOut(t *testing.T) {
	now := time.Now().UTC()
	l := New(Config{ID: "a"})
	l.RegisterPush("old0", "production", now.Add(-notify.PushTTL-time.Hour))
	l.RegisterPush("new1", "production", now)

	l.mu.Lock()
	l.prunePush(now)
	l.mu.Unlock()

	regs := l.PushRegistrations()
	if len(regs) != 1 || regs[0].Token != "new1" {
		t.Fatalf("pruning kept the wrong registrations: %+v", regs)
	}
}

func TestPushPayloadEmptyWhenNoPhones(t *testing.T) {
	l := New(Config{ID: "a"})
	if l.pushPayload() != nil {
		t.Fatal("no phones means no extra gossip packet")
	}
}
