package categorization

import (
	"sort"
	"strings"
)

type Matcher struct {
	rules []Rule
}

// NewMatcher orders rules so the first hit wins: lower priority number first,
// then the more specific rule, then the older one.
func NewMatcher(rules []Rule) *Matcher {
	sorted := append([]Rule(nil), rules...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}
		if si, sj := specificity(sorted[i]), specificity(sorted[j]); si != sj {
			return si > sj
		}
		return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
	})
	return &Matcher{rules: sorted}
}

func (m *Matcher) Match(c Candidate) *Rule {
	for i := range m.rules {
		if matches(m.rules[i], c) {
			return &m.rules[i]
		}
	}
	return nil
}

func matches(r Rule, c Candidate) bool {
	return equalOrAny(r.MatchCounterparty, c.Counterparty) &&
		containsOrAny(r.MatchDescription, c.Description) &&
		equalOrAny(r.MatchCounterpartyAccount, c.CounterpartyAccount) &&
		equalOrAny(r.MatchOperationType, c.OperationType) &&
		symbolEqualOrAny(r.MatchVariableSymbol, c.VariableSymbol)
}

// symbolEqualOrAny ignores leading zeros: banks pad symbols to their field width, rules are typed without.
func symbolEqualOrAny(want *string, got string) bool {
	return want == nil || strings.TrimLeft(strings.TrimSpace(*want), "0") == strings.TrimLeft(strings.TrimSpace(got), "0")
}

func equalOrAny(want *string, got string) bool {
	return want == nil || normalize(*want) == normalize(got)
}

func containsOrAny(want *string, got string) bool {
	return want == nil || strings.Contains(normalize(got), normalize(*want))
}

func normalize(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func specificity(r Rule) int {
	n := 0
	for _, p := range []*string{r.MatchCounterparty, r.MatchDescription, r.MatchCounterpartyAccount, r.MatchOperationType, r.MatchVariableSymbol} {
		if p != nil {
			n++
		}
	}
	return n
}

func (m *Matcher) Rules() []Rule { return m.rules }
