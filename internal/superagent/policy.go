package superagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// AcceptMode controls what happens to inferred candidates.
type AcceptMode string

const (
	// AcceptOff: nothing inferred becomes a candidate.
	AcceptOff AcceptMode = "off"
	// AcceptObserve: inferred candidates are assessed; a positive assessment is recorded as
	// ignore marked "would accept", and nothing is accepted.
	AcceptObserve AcceptMode = "observe"
	// AcceptAuto: a positive assessment of an inferred candidate accepts it.
	AcceptAuto AcceptMode = "auto"
)

// Cell is one choice of an admission table row.
type Cell string

const (
	CellExplicit Cell = "explicit"
	CellInferred Cell = "inferred"
	CellIgnore   Cell = "ignore"
)

// Admission is one row of the admission table: how messages in a receiving scope are admitted.
type Admission struct {
	ListedUnaddressed   Cell `json:"listed_unaddressed,omitempty"`   // inferred | ignore (default inferred)
	UnlistedAddressed   Cell `json:"unlisted_addressed,omitempty"`   // explicit | inferred | ignore (default ignore)
	UnlistedUnaddressed Cell `json:"unlisted_unaddressed,omitempty"` // inferred | ignore (default ignore)
}

func (a Admission) withDefaults() Admission {
	if a.ListedUnaddressed == "" {
		a.ListedUnaddressed = CellInferred
	}
	if a.UnlistedAddressed == "" {
		a.UnlistedAddressed = CellIgnore
	}
	if a.UnlistedUnaddressed == "" {
		a.UnlistedUnaddressed = CellIgnore
	}
	return a
}

// SourcePolicy is a Source's part of the Policy, in that Source's own identities.
type SourcePolicy struct {
	// Allowed lists the people who may originate work. An empty list allows every person,
	// matching the chat-mode allowlist semantics.
	Allowed []string `json:"allowed,omitempty"`
	// Bots lists bots whose messages may become inferred candidates.
	Bots []string `json:"bots,omitempty"`
	// Default is the admission row for scopes without an explicit row.
	Default Admission `json:"default"`
	// Scopes holds per-scope admission rows (e.g. per chat id).
	Scopes map[string]Admission `json:"scopes,omitempty"`
}

// Listed reports whether the actor is on the allowed list.
func (s SourcePolicy) Listed(a Actor) bool {
	if len(s.Allowed) == 0 {
		return true
	}
	return matchAny(s.Allowed, a.Identities())
}

// ListedBot reports whether the actor is on the bot list.
func (s SourcePolicy) ListedBot(a Actor) bool { return matchAny(s.Bots, a.Identities()) }

// Row returns the admission row for a receiving scope.
func (s SourcePolicy) Row(scope string) Admission {
	if r, ok := s.Scopes[scope]; ok {
		return r.withDefaults()
	}
	return s.Default.withDefaults()
}

// Rule describes one kind of proactive work.
type Rule struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	Enabled      bool   `json:"enabled"`
}

// Policy is the versioned set of rules for accepting work.
type Policy struct {
	AcceptMode AcceptMode              `json:"accept_mode"`
	Rules      []Rule                  `json:"rules,omitempty"`
	Sources    map[string]SourcePolicy `json:"sources,omitempty"`
}

// Mode returns the accept mode, defaulting to observe.
func (p Policy) Mode() AcceptMode {
	switch p.AcceptMode {
	case AcceptOff, AcceptAuto:
		return p.AcceptMode
	}
	return AcceptObserve
}

// Version is a stable hash of the Policy; every Mandate records it.
func (p Policy) Version() string {
	cp := p
	cp.AcceptMode = p.Mode()
	raw, _ := json.Marshal(cp) // map keys are marshalled in sorted order
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:12]
}

// Source returns a Source's part of the Policy.
func (p Policy) Source(name string) SourcePolicy { return p.Sources[name] }

// EnabledRules returns the enabled rules.
func (p Policy) EnabledRules() []Rule {
	var out []Rule
	for _, r := range p.Rules {
		if r.Enabled && strings.TrimSpace(r.Name) != "" {
			out = append(out, r)
		}
	}
	return out
}

// Rule returns an enabled rule by name.
func (p Policy) Rule(name string) (Rule, bool) {
	for _, r := range p.EnabledRules() {
		if strings.EqualFold(r.Name, name) {
			return r, true
		}
	}
	return Rule{}, false
}

// Gate is the deterministic first layer of Eval: who may originate work, and with what strength.
// followup reports whether the Trigger is a person's message on a Thread that already has Tickets.
func (p Policy) Gate(t Trigger, followup bool) (Strength, Basis) {
	sp := p.Source(t.Source)
	addressed := t.Addressed != AddressNone
	mode := p.Mode()
	inferred := func(b Basis) (Strength, Basis) {
		if mode == AcceptOff {
			return StrengthNone, ""
		}
		return StrengthInferred, b
	}

	if t.Actor.Bot {
		// A bot on the bot list can produce an inferred candidate, never an explicit request.
		if sp.ListedBot(t.Actor) && addressed {
			return inferred(BasisBot)
		}
		return StrengthNone, ""
	}

	listed := sp.Listed(t.Actor)
	row := sp.Row(t.Scope)

	if listed && addressed {
		return StrengthExplicit, BasisListed
	}
	if followup {
		// An explicit admission cell takes precedence even on a Thread with Tickets.
		if !listed && addressed && row.UnlistedAddressed == CellExplicit {
			return StrengthExplicit, BasisTable
		}
		if listed {
			return StrengthExplicit, BasisFollowup
		}
		return inferred(BasisFollowup)
	}

	var cell Cell
	switch {
	case listed:
		cell = row.ListedUnaddressed
		if cell == CellExplicit { // not a valid choice for unaddressed messages
			cell = CellInferred
		}
	case addressed:
		cell = row.UnlistedAddressed
	default:
		cell = row.UnlistedUnaddressed
		if cell == CellExplicit {
			cell = CellInferred
		}
	}
	switch cell {
	case CellExplicit:
		return StrengthExplicit, BasisTable
	case CellInferred:
		return inferred(BasisTable)
	}
	return StrengthNone, ""
}

func matchAny(list []string, ids []string) bool {
	for _, raw := range list {
		allowed := normID(raw)
		if allowed == "" {
			continue
		}
		if allowed == "*" {
			return true
		}
		if slices.ContainsFunc(ids, func(id string) bool { return normID(id) == allowed }) {
			return true
		}
	}
	return false
}

func normID(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "@")
}
