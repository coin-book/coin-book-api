package categorization

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifierGeneralisesAcrossShopNames(t *testing.T) {
	groceries, dining, fuel := uuid.New(), uuid.New(), uuid.New()
	example := func(counterparty string, amount int64, category uuid.UUID, source Source) Example {
		return Example{Candidate: Candidate{Counterparty: counterparty, Description: counterparty + " PLATBA KARTOU",
			OperationType: "PLATBA KARTOU", AmountMinor: amount}, CategoryID: category, Source: source}
	}
	model := Train([]Example{
		example("ALBERT PRAHA", -45000, groceries, SourceManual),
		example("ALBERT HM", -120000, groceries, SourceBank),
		example("LIDL DEKUJEME", -80000, groceries, SourceRule),
		example("BISTRO NA ROHU", -29000, dining, SourceManual),
		example("PIZZERIA TESTOVA", -35000, dining, SourceManual),
		example("BENZINA TEST", -150000, fuel, SourceManual),
		example("SHELL", -160000, fuel, SourceBank),
	})

	got := model.Predict(Candidate{Counterparty: "ALBERT BRNO", Description: "ALBERT BRNO PLATBA KARTOU",
		OperationType: "PLATBA KARTOU", AmountMinor: -60000}, 3)

	require.NotEmpty(t, got)
	assert.Equal(t, groceries, got[0].CategoryID)
	assert.Contains(t, got[0].Reasons, "albert")
	assert.Greater(t, got[0].Confidence, got[1].Confidence)
}

func TestClassifierIsNotSureWithoutKnownWords(t *testing.T) {
	shopping := uuid.New()
	model := Train([]Example{{Candidate: Candidate{Counterparty: "OBCHOD", OperationType: "merchant", AmountMinor: -20000},
		CategoryID: shopping, Source: SourceManual}})

	got := model.Predict(Candidate{Counterparty: "PIZZERIA TESTOVA", OperationType: "merchant", AmountMinor: -11000}, 1)

	require.Len(t, got, 1)
	assert.Equal(t, shopping, got[0].CategoryID, "still the best guess")
	assert.Less(t, got[0].Confidence, 0.05, "but nothing it knows backs it")
}

func TestClassifierIgnoresItsOwnGuesses(t *testing.T) {
	model := Train([]Example{{Candidate: Candidate{Counterparty: "X"}, CategoryID: uuid.New(), Source: SourceModel}})
	assert.Zero(t, model.Examples())
	assert.Empty(t, model.Predict(Candidate{Counterparty: "X"}, 3))
}

func TestFeaturesDropNoiseAndSeparateFields(t *testing.T) {
	f := Features(Candidate{Counterparty: "ROHLIK.CZ", Description: "ROHLIK.CZ PLATBA KARTOU", OperationType: "PLATBA KARTOU", AmountMinor: -124900})
	assert.Contains(t, f, "c:rohlik.cz")
	assert.NotContains(t, f, "d:platba")
	assert.Contains(t, f, "op:platba kartou")
	assert.Contains(t, f, "amt:3")
}
