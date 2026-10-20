package portu

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/testutil"
)

const export = "testdata/Transakcni_historie.csv"

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func parse(t *testing.T) *statement.Statement {
	t.Helper()
	raw, err := os.ReadFile(export)
	require.NoError(t, err)
	st, err := Parser{}.Parse(bytes.NewReader(raw))
	require.NoError(t, err)
	return st
}

func TestDetectsTheExport(t *testing.T) {
	raw, err := os.ReadFile(export)
	require.NoError(t, err)
	assert.Equal(t, statement.Certain, Parser{}.Detect(raw))
	assert.Equal(t, statement.NoMatch, Parser{}.Detect([]byte("Datum;Popis;Částka\n")))
}

func TestParseGolden(t *testing.T) {
	testutil.AssertGoldenJSON(t, "testdata/statement.golden.json", parse(t))
}

func TestTradesAreValuedInCrownsAtPortusOwnExchange(t *testing.T) {
	inv := parse(t).Accounts[0].Investments
	require.Len(t, inv.Buys, 3, "deposits, exchanges, fees and cash moves are not trades")
	byDay := map[time.Time]statement.Buy{}
	for _, b := range inv.Buys {
		byDay[b.TradedOn] = b
	}
	assert.Equal(t, int64(860_000), *byDay[day(2022, 2, 2)].NetCZKMinor, "400 $ at the 2022 swap's 21.50")
	assert.Equal(t, int64(1_144_000), *byDay[day(2026, 1, 5)].NetCZKMinor, "520 $ at 22.00")
	assert.Equal(t, int64(200_000), *byDay[day(2026, 1, 4)].NetCZKMinor)
	assert.Equal(t, "CSPX.L", byDay[day(2026, 1, 5)].Security.YahooSymbol)
	assert.Equal(t, "CEZ.PR", byDay[day(2026, 1, 4)].Security.YahooSymbol)
}

func TestASaleTakesTheOldestLotIncludingOneMovedFromAnotherPortfolio(t *testing.T) {
	sales := parse(t).Accounts[0].Investments.LotSales
	require.Len(t, sales, 2)
	moved, bought := sales[0], sales[1]
	assert.Equal(t, day(2022, 2, 2), moved.BoughtOn, "the lot moved from Rezerva keeps its purchase day")
	assert.Equal(t, "1", moved.Quantity.String())
	assert.Equal(t, int64(60_000), moved.ProceedsMinor)
	assert.Equal(t, int64(40_000), moved.CostMinor)
	assert.Equal(t, day(2026, 1, 5), bought.BoughtOn)
	assert.Equal(t, "0.5", bought.Quantity.String())
	assert.Equal(t, int64(30_000), bought.ProceedsMinor)
	assert.Equal(t, int64(26_000), bought.CostMinor)
	require.NotNil(t, moved.CostCZKMinor)
	assert.Equal(t, int64(860_000), *moved.CostCZKMinor)
}

func TestDividendWithItsWithholdingTax(t *testing.T) {
	divs := parse(t).Accounts[0].Investments.Dividends
	require.Len(t, divs, 1)
	assert.Equal(t, int64(1000), divs[0].GrossMinor)
	assert.Equal(t, int64(850), divs[0].NetMinor)
	assert.NotNil(t, divs[0].NetCZKMinor, "the nearest exchange within a week values it")
}

func TestRefusesExponentsAndASaleBeyondTheHolding(t *testing.T) {
	raw, err := os.ReadFile(export)
	require.NoError(t, err)
	exponent := strings.Replace(string(raw), "520,00;USD", "1e-2000000000;USD", 1)
	_, err = Parser{}.Parse(strings.NewReader(exponent))
	assert.ErrorIs(t, err, ErrInvalidValue)

	moveTooMuch := strings.Replace(string(raw), "Rezerva;Převod do Důchod – CSPX LN – 1 kus;CSPX LN;IE00B5BMR087;1,0000",
		"Rezerva;Převod do Důchod – CSPX LN – 1 kus;CSPX LN;IE00B5BMR087;5,0000", 1)
	_, err = Parser{}.Parse(strings.NewReader(moveTooMuch))
	assert.ErrorIs(t, err, ErrInvalidValue)
}

func TestLotsMovedBackAndForthStopAtTheWorkBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Join(header, ";") + "\r\n")
	const buys, moves = 2000, 600
	for i := range moves {
		from, to := "A", "B"
		if i%2 == 1 {
			from, to = "B", "A"
		}
		b.WriteString("2.2.2026;" + from + ";Převod do " + to + " – CSPX LN – 2000 kusů;CSPX LN;IE00B5BMR087;2000,0000;;1,00;-2000,00;USD;;\r\n")
	}
	for range buys {
		b.WriteString("1.2.2026;A;Nákup – CSPX LN – 1 kus (1 $ za kus);CSPX LN;IE00B5BMR087;1,0000;;1,00;-1,00;USD;;\r\n")
	}
	started := time.Now()
	_, err := Parser{}.Parse(strings.NewReader(b.String()))
	assert.ErrorIs(t, err, ErrInvalidValue)
	assert.Less(t, time.Since(started), 5*time.Second)
}

func TestACrownValueBeyondInt64StaysUnknown(t *testing.T) {
	r := &replay{rates: map[rateKey]decimal.Decimal{{day(2026, 1, 1), "JPY"}: decimal.RequireFromString("9999999999999")}}
	assert.Nil(t, r.toCZK(999_999_999_999_999, "JPY", day(2026, 1, 1)))
}

func TestManyTinyMovesIntoALongListStopAtTheWorkBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Join(header, ";") + "\r\n")
	const n = 20_000
	for range n {
		b.WriteString("3.2.2026;A;Převod do B – CSPX LN – 0 kusů;CSPX LN;IE00B5BMR087;0,0001;;1,00;-0,01;USD;;\r\n")
	}
	for range n {
		b.WriteString("2.2.2026;B;Nákup – CSPX LN – 1 kus (1 $ za kus);CSPX LN;IE00B5BMR087;1,0000;;1,00;-1,00;USD;;\r\n")
	}
	b.WriteString("1.2.2026;A;Nákup – CSPX LN – 10 kusů (1 $ za kus);CSPX LN;IE00B5BMR087;10,0000;;1,00;-10,00;USD;;\r\n")
	started := time.Now()
	_, err := Parser{}.Parse(strings.NewReader(b.String()))
	assert.ErrorIs(t, err, ErrInvalidValue)
	assert.Less(t, time.Since(started), 3*time.Second)
}
