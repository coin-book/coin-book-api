package importing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"coin-book-api/internal/domain/importing/statement"
)

func day(d string) time.Time {
	t, _ := time.Parse(time.DateOnly, d)
	return t
}

func TestClosestSamePaymentPrefersTheNearestDayAndTheSamePayee(t *testing.T) {
	far := &otherStatementRow{bookedOn: day("2026-10-09"), amountMinor: -124900, description: "ROHLIK.CZ PLATBA KARTOU"}
	near := &otherStatementRow{bookedOn: day("2026-10-04"), amountMinor: -124900, description: "ROHLIK.CZ PLATBA KARTOU"}
	otherPayee := &otherStatementRow{bookedOn: day("2026-10-02"), amountMinor: -124900, description: "KOSIK.CZ PLATBA KARTOU"}
	row := statement.Row{BookedOn: day("2026-10-02"), AmountMinor: -124900, Counterparty: "Rohlik.cz Praha"}

	assert.Same(t, near, closestSamePayment([]*otherStatementRow{far, otherPayee, near}, row))
	near.taken = true
	assert.Nil(t, closestSamePayment([]*otherStatementRow{near, otherPayee}, row), "one stored row stands for one incoming row")
}

func TestSamePayeeByCounterpartyAccount(t *testing.T) {
	stored := &otherStatementRow{counterpartyAccount: "19-2000000001/0800", description: "VS:2026001 FIRMA TEST"}

	assert.True(t, samePayee(statement.Row{CounterpartyAccount: "19-2000000001/0800", Counterparty: "Jiny nazev"}, stored))
	assert.False(t, samePayee(statement.Row{Counterparty: "FIR"}, stored), "a word shorter than four letters proves nothing")
}
