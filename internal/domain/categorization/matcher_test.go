package categorization

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr(s string) *string { return &s }

func TestMatcherPrefersSpecificRuleAtSamePriority(t *testing.T) {
	groceries, dining := uuid.New(), uuid.New()
	m := NewMatcher([]Rule{
		{CategoryID: groceries, Priority: 100, MatchDescription: ptr("rohlik"), CreatedAt: time.Unix(1, 0)},
		{CategoryID: dining, Priority: 100, MatchDescription: ptr("rohlik"), MatchOperationType: ptr("PLATBA KARTOU"), CreatedAt: time.Unix(2, 0)},
	})

	got := m.Match(Candidate{Description: "ROHLIK.CZ PLATBA KARTOU", OperationType: "platba  kartou"})

	require.NotNil(t, got)
	assert.Equal(t, dining, got.CategoryID)
}

func TestMatcherCounterpartyIsExactNotSubstring(t *testing.T) {
	m := NewMatcher([]Rule{{CategoryID: uuid.New(), MatchCounterparty: ptr("Albert")}})

	assert.NotNil(t, m.Match(Candidate{Counterparty: "ALBERT"}))
	assert.Nil(t, m.Match(Candidate{Counterparty: "ALBERT HEIJN"}))
}

func TestBankHintsPointAtSeededCodes(t *testing.T) {
	for bank, code := range bankCategoryHints {
		assert.NotEmpty(t, code, bank)
	}
}
