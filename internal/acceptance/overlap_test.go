//go:build integration

package acceptance

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aboAccount is ownMBankAccount as ABO writes it: 6 + 10 digits, no bank code.
const aboAccount = "0000191234567890"

type aboRow struct {
	day          string
	name         string
	counterparty string
	bankCode     string
	minor        int64
}

func aboStatement(t *testing.T, rows []aboRow) []byte {
	t.Helper()
	const opening = int64(10000000)
	closing, debit, credit := opening, int64(0), int64(0)
	for _, r := range rows {
		closing += r.minor
		if r.minor < 0 {
			debit -= r.minor
		} else {
			credit += r.minor
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "074%s%-20s300926%014d+%014d+%014d0%014d0001311026%14s\r\n",
		aboAccount, "Test ucet", opening, closing, debit, credit, "")
	for i, r := range rows {
		day, err := time.Parse(time.DateOnly, r.day)
		require.NoError(t, err)
		code, amount := "2", r.minor
		if amount < 0 {
			code, amount = "1", -amount
		}
		date := day.Format("020106")
		fmt.Fprintf(&b, "075%s%016s%013d%012d%s%010d00%04s%04d%010d%s%-20s00203%s\r\n",
			aboAccount, r.counterparty, i+1, amount, code, 0, r.bankCode, 0, 0, date, r.name, date)
	}
	return []byte(b.String())
}

func TestABOStatementOfAnImportedMBankAccountAddsOnlyWhatIsMissing(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows[:4]), http.StatusCreated, nil)

	abo := aboStatement(t, []aboRow{
		{"2026-10-04", "ROHLIK.CZ", "", "", -124900},
		{"2026-10-05", "ROHLIK.CZ", "", "", -124900},
		{"2026-10-07", "KAVARNA U TESTU", "", "", -8900},
		{"2026-10-10", "Firma Test", "0000192000000001", "0800", 6200000},
		{"2026-10-12", "Revolut dobiti", "", "", -500000},
		{"2026-10-15", "LEKARNA", "", "", -45000},
	})
	var preview importResult
	c.upload("/api/v1/imports/preview", "vypis.gpc", abo, http.StatusOK, &preview)
	require.Len(t, preview.Accounts, 1)
	assert.Equal(t, "identity", preview.Accounts[0].MatchedBy, "the number without its bank code names the mBank account")
	assert.Equal(t, 4, preview.RowsInOther, "card payments booked two days later are the same payments")
	assert.Equal(t, 2, preview.RowsNew, "a second identical payment is not swallowed by the first")

	var done importResult
	c.upload("/api/v1/imports", "vypis.gpc", abo, http.StatusCreated, &done)
	assert.Equal(t, 2, done.RowsNew)
	assert.Len(t, c.transactions("").Transactions, 6)
	assert.Len(t, c.accounts(), 1)
}

func TestReimportByANewerParserFillsInWhatTheOlderOneMissed(t *testing.T) {
	c := newClient(t)
	rent := row{"2026-10-15", "NAJEMNE RIJEN KS:0308 VS:0002026010  PRONAJIMATEL S.R.O.  000000 1234567890 0100", "Bydlení", -1500000}
	statement := mbankStatement(t, ownMBankAccount, []row{rent})
	var first importResult
	c.upload("/api/v1/imports", "mbank.csv", statement, http.StatusCreated, &first)

	ctx := context.Background()
	var userID uuid.UUID
	require.NoError(t, pg.Pool.QueryRow(ctx, `SELECT id FROM app_user WHERE email = 'test@coinbook.local'`).Scan(&userID))
	scoped, release, err := pg.Pool.ScopeToUser(ctx, userID)
	require.NoError(t, err)
	defer release()
	_, err = pg.Pool.Exec(scoped, `UPDATE import_batch SET parser_version = 1 WHERE id = $1`, first.BatchID)
	require.NoError(t, err)
	_, err = pg.Pool.Exec(scoped, `UPDATE transaction SET variable_symbol = NULL, constant_symbol = NULL,
		counterparty = 'NAJEMNE RIJEN KS:0308 VS:0002026010'`)
	require.NoError(t, err)

	var again importResult
	c.upload("/api/v1/imports", "mbank.csv", statement, http.StatusCreated, &again)
	assert.Zero(t, again.RowsNew)
	assert.Equal(t, 1, again.RowsRefreshed)

	var vs, ks, counterparty string
	require.NoError(t, pg.Pool.QueryRow(scoped, `SELECT variable_symbol, constant_symbol, counterparty FROM transaction`).
		Scan(&vs, &ks, &counterparty))
	assert.Equal(t, "2026010", vs)
	assert.Equal(t, "308", ks)
	assert.Equal(t, "NAJEMNE RIJEN", counterparty)
}
