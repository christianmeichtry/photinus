package lantern

import (
	"fmt"
	"sort"
	"strings"

	"github.com/christianmeichtry/photinus/internal/quorum"
)

// A blackout is many watched services at one address going dark together.
//
// Tiers answer "does this subject deserve a page", one subject at a time,
// and that is the right question for one subject. It is the wrong question
// for a machine: nine routine sites are nine small matters, but nine
// routine sites on one host all falling silent in the same minute is one
// large one, and no subject carries that fact. So the lantern reads it off
// the group instead of the member, and pages for the group regardless of
// what tier its members were given.
//
// The address comes from the connections the checks themselves made, so
// there is nothing to declare and nothing to keep in step with reality:
// move a site to another machine and the grouping follows on the next
// successful probe. Nothing is gossiped for this. Every lantern already
// holds the swarm's verdicts and its own address book, which is all the
// rule reads, so it survives any single lantern vanishing (rule 2), and
// the usual hash election still means exactly one lantern pages.

// blackoutSites is the smallest number of distinct sites a blackout must
// span. Two subjects are commonly the http and cert of one site, and one
// site being down is not a machine being down.
const blackoutSites = 2

// defaultBlackout is how many dark subjects at one address make a blackout
// when the operator says nothing. Three, because two are routinely the
// http and the cert of a single site.
const defaultBlackout = 3

// blackout returns one synthetic decision per address that has gone
// entirely dark. Grouping only ever considers subjects whose address this
// lantern learned first hand; a subject it has never reached carries no
// address and takes no part.
//
// The rule is deliberately strict: every watched subject at the address
// must be down, and there must be at least min of them across at least two
// sites. A partly broken host still has a healthy vhost answering, and
// that is the per-subject case the tiers already handle. What this catches
// is the address that stopped answering at all.
// Every address big enough to be capable of a blackout gets a decision,
// dark or not. A group that is merely healthy must still be spoken about,
// or the notification tracker would never hear a blackout end: the alarm
// would stay open forever and, worse, a second blackout at that address
// would look like no change at all and page nobody. The healthy ones carry
// state up and are kept off the panel by the caller, since a lantern does
// not announce good news.
func blackout(subjects []SubjectStatus, addrs map[string]string, min int) []quorum.Decision {
	if min <= 0 {
		return nil
	}
	type group struct {
		down, known int
		sites       map[string]bool
		darkSites   map[string]bool
	}
	groups := map[string]*group{}
	for _, s := range subjects {
		addr, ok := addrs[s.Check+" "+s.Target]
		if !ok || addr == "" {
			continue
		}
		g := groups[addr]
		if g == nil {
			g = &group{sites: map[string]bool{}, darkSites: map[string]bool{}}
			groups[addr] = g
		}
		g.known++
		g.sites[siteOf(s.Target)] = true
		// A subject nobody has a live word on says nothing either way, and
		// a blackout must not be declared on silence.
		if s.State != quorum.StateDown {
			continue
		}
		g.down++
		g.darkSites[siteOf(s.Target)] = true
	}

	var out []quorum.Decision
	for addr, g := range groups {
		if g.known < min || len(g.sites) < blackoutSites {
			continue // too small to ever be a blackout
		}
		d := quorum.Decision{
			Check:  "blackout",
			Target: addr,
			State:  quorum.StateUp,
			// The members already reached quorum on their own; the group is
			// read off those verdicts, so it inherits their agreement. The
			// counts describe the group, which is what an operator wants to
			// see, and they keep the tracker from treating it as unknown.
			Votes:  g.down,
			Voters: g.known,
			Needed: min,
		}
		if g.down == g.known && g.down >= min && len(g.darkSites) >= blackoutSites {
			dark := make([]string, 0, len(g.darkSites))
			for s := range g.darkSites {
				dark = append(dark, s)
			}
			sort.Strings(dark)
			d.State = quorum.StateDown
			d.Detail = fmt.Sprintf("every watched service at this address is down, %d across %d sites: %s",
				g.down, len(dark), strings.Join(dark, ", "))
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// siteOf tidies a target down to the machine-facing name a human would use,
// so that the http and the cert of one site count once. It mirrors the
// panel's grouping: scheme and path go, the implicit https port goes, a
// meaningful port stays.
func siteOf(target string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(target, "https://"), "http://")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, ":443")
}
