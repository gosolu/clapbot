package superagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
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

// WithAcceptMode returns a copy of Policy with the specified AcceptMode.
func (p Policy) WithAcceptMode(mode AcceptMode) Policy {
	cp := p
	cp.AcceptMode = mode
	return cp
}

// WithRule returns a copy of Policy with the given rule added or updated.
func (p Policy) WithRule(rule Rule) Policy {
	cp := p
	found := false
	var newRules []Rule
	for _, r := range p.Rules {
		if strings.EqualFold(r.Name, rule.Name) {
			newRules = append(newRules, rule)
			found = true
		} else {
			newRules = append(newRules, r)
		}
	}
	if !found {
		newRules = append(newRules, rule)
	}
	cp.Rules = newRules
	return cp
}

// WithRuleEnabled returns a copy of Policy with the named rule enabled or disabled.
func (p Policy) WithRuleEnabled(name string, enabled bool) Policy {
	cp := p
	var newRules []Rule
	for _, r := range p.Rules {
		if strings.EqualFold(r.Name, name) {
			r.Enabled = enabled
		}
		newRules = append(newRules, r)
	}
	cp.Rules = newRules
	return cp
}

// RuleEffect describes how a policy rule change affects a ticket.
type RuleEffect struct {
	Name        string `json:"name"`
	Change      string `json:"change"` // "changed" | "disabled"
	Instruction string `json:"instruction,omitempty"`
}

// PolicyEffect describes how a policy change bears on an open ticket.
type PolicyEffect struct {
	Rules            []RuleEffect `json:"rules,omitempty"`
	Proactive        *bool        `json:"proactive,omitempty"`         // false if inferred work is no longer accepted
	RequesterAllowed *bool        `json:"requester_allowed,omitempty"` // false if requester may no longer start work
	Requester        Actor        `json:"requester,omitempty"`
}

// FormatPolicyMessage formats a human-readable notice for an executor session
// describing how policy changes affect a ticket.
func FormatPolicyMessage(effect PolicyEffect) string {
	var lines []string
	lines = append(lines, "Policy change:")

	for _, rule := range effect.Rules {
		if rule.Change == "disabled" {
			lines = append(lines, fmt.Sprintf("Rule %s, which this ticket was accepted under, no longer applies: finish only a change within it, or report dropped.", rule.Name))
		} else {
			lines = append(lines, fmt.Sprintf("Rule %s, which this ticket was accepted under, changed. Keep the remaining work within it as it reads now: %s", rule.Name, rule.Instruction))
		}
	}

	if effect.Proactive != nil && !*effect.Proactive {
		lines = append(lines, "Work inferred from a rule is no longer accepted, as the accept mode is no longer auto: finish only what is in progress within the assignment, or report dropped, and take on nothing new.")
	}

	if effect.RequesterAllowed != nil && !*effect.RequesterAllowed {
		reqLabel := effect.Requester.Label()
		if reqLabel == "" {
			reqLabel = "the requester"
		}
		lines = append(lines, fmt.Sprintf("The sender this ticket is for, %s, may no longer start work here: finish the assignment within its scope, and take no new requests from them on it.", reqLabel))
	}

	if len(lines) == 1 {
		return "Policy change: the access or admission rules changed. Check that the remaining work stays within the assignment."
	}
	return strings.Join(lines, "\n")
}

// ComputePolicyEffects calculates how a transition from oldPol to newPol affects open tickets.
func ComputePolicyEffects(tickets []*Ticket, oldPol, newPol Policy) map[string]PolicyEffect {
	effects := make(map[string]PolicyEffect)
	oldMode := oldPol.Mode()
	newMode := newPol.Mode()

	for _, t := range tickets {
		if !t.Live() {
			continue
		}
		var (
			ruleEffects      []RuleEffect
			proactive        *bool
			requesterAllowed *bool
		)

		// 1. Check matched rules
		for _, ruleName := range t.Mandate.MatchedRules {
			oldRule, oldFound := oldPol.Rule(ruleName)
			newRule, newFound := newPol.Rule(ruleName)

			if oldFound && (!newFound || !newRule.Enabled) {
				ruleEffects = append(ruleEffects, RuleEffect{
					Name:   ruleName,
					Change: "disabled",
				})
			} else if oldFound && newFound {
				if oldRule.Instructions != newRule.Instructions {
					ruleEffects = append(ruleEffects, RuleEffect{
						Name:        ruleName,
						Change:      "changed",
						Instruction: newRule.Instructions,
					})
				}
			}
		}

		// 2. Check proactive mode
		if t.Mandate.Strength == StrengthInferred && oldMode == AcceptAuto && newMode != AcceptAuto {
			val := false
			proactive = &val
		}

		// 3. Check requester authorization
		spOld := oldPol.Source(t.Mandate.Source)
		spNew := newPol.Source(t.Mandate.Source)
		if spOld.Listed(t.Mandate.Requester) && !spNew.Listed(t.Mandate.Requester) {
			val := false
			requesterAllowed = &val
		}

		if len(ruleEffects) > 0 || proactive != nil || requesterAllowed != nil {
			effects[t.ID] = PolicyEffect{
				Rules:            ruleEffects,
				Proactive:        proactive,
				RequesterAllowed: requesterAllowed,
				Requester:        t.Mandate.Requester,
			}
		}
	}
	return effects
}

// MutablePolicyProvider is a PolicyProvider that supports dynamic updates at runtime.
type MutablePolicyProvider interface {
	PolicyProvider
	SetPolicy(p Policy)
}

// DynamicPolicyProvider is a thread-safe PolicyProvider implementation.
type DynamicPolicyProvider struct {
	mu sync.RWMutex
	p  Policy
}

// NewDynamicPolicyProvider constructs a DynamicPolicyProvider with initial policy.
func NewDynamicPolicyProvider(initial Policy) *DynamicPolicyProvider {
	return &DynamicPolicyProvider{p: initial}
}

// CurrentPolicy returns the active Policy.
func (d *DynamicPolicyProvider) CurrentPolicy() Policy {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.p
}

// SetPolicy updates the active Policy.
func (d *DynamicPolicyProvider) SetPolicy(p Policy) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.p = p
}
