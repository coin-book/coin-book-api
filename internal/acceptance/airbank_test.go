//go:build integration

package acceptance

import (
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAirBankExportNamesItsAccountInTheFileName(t *testing.T) {
	c := newClient(t)
	const name = "airbank_1234567890_2026-10-18_12-00.csv"
	content, err := os.ReadFile("../domain/importing/parser/airbank/testdata/" + name)
	require.NoError(t, err)

	var first importResult
	c.upload("/api/v1/imports", name, content, http.StatusCreated, &first)
	assert.Equal(t, "airbank-cz", first.Format)
	assert.Equal(t, 3, first.RowsNew)

	accounts := c.bankAccounts()
	require.Len(t, accounts, 1)
	airbank := accounts["Air Bank CZK"]
	require.NotNil(t, airbank.BankID)
	assert.Equal(t, "cz-3030", *airbank.BankID)

	var again importResult
	c.upload("/api/v1/imports", "airbank_1234567890_2026-11-18_12-00.csv", append(content, '\n'), http.StatusCreated, &again)
	assert.Zero(t, again.RowsNew)
	assert.Equal(t, 3, again.RowsDuplicate)
	assert.Len(t, c.bankAccounts(), 1, "the number in the file name finds the same account")
}
