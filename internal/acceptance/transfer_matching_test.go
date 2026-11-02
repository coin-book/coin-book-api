//go:build integration

package acceptance

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const eurInCZK = 25

// revolutEURStatement writes a Revolut EUR account, whose every money column repeats in CZK.
func revolutEURStatement(rows []row) []byte {
	var b bytes.Buffer
	b.WriteString("Bežné účty Výpisy o transakciách\n\nOsobný účet (EUR)\n\nVýpis transakcií\n")
	b.WriteString("Dátum,Popis,Kategória,Pohyb peňazí,Pohyb peňazí,Zostatok,Zostatok,Zrazená daň,Zrazená daň," +
		"Iné dane,Iné dane,Poplatky,Poplatky\n")
	var balance int64
	for _, r := range rows {
		balance += r.minor
		d, _ := time.Parse(time.DateOnly, r.day)
		fmt.Fprintf(&b, "%d. %d. %d,%s,%s,\"%s€\",\"%s CZK\",\"%s€\",\"%s CZK\",\"0,00€\",\"0,00 CZK\",\"0,00€\",\"0,00 CZK\",\"0,00€\",\"0,00 CZK\"\n",
			d.Day(), int(d.Month()), d.Year(), r.description, r.bankCat, decimal(r.minor), decimal(r.minor*eurInCZK),
			decimal(balance), decimal(balance*eurInCZK))
	}
	fmt.Fprintf(&b, "Spolu,,,\"%s€\",\"%s CZK\",,,\"0,00€\",\"0,00 CZK\",\"0,00€\",\"0,00 CZK\",\"0,00€\",\"0,00 CZK\"\n---------\n",
		decimal(balance), decimal(balance*eurInCZK))
	return b.Bytes()
}

func (c *client) transactionByID(id string) legView {
	c.t.Helper()
	var list struct {
		Transactions []legView `json:"transactions"`
	}
	c.do(http.MethodGet, "/api/v1/transactions?limit=200", nil, http.StatusOK, &list)
	for _, tx := range list.Transactions {
		if tx.ID == id {
			return tx
		}
	}
	c.t.Fatalf("transaction %s not listed", id)
	return legView{}
}

func TestExchangeBetweenRevolutCurrencyAccountsIsPaired(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "czk.csv", revolutStatement(1000000, []row{
		{"2026-10-12", "Zamenené na EUR", "Zameniť", -250000},
	}), http.StatusCreated, nil)

	var eur importResult
	c.upload("/api/v1/imports", "eur.csv", revolutEURStatement([]row{
		{"2026-10-12", "Zamenené z CZK", "Zameniť", 10000},
	}), http.StatusCreated, &eur)
	assert.Equal(t, 1, eur.TransfersMatched, "the amounts differ; the same day and the exchange type pair them")
}

func TestTransferToAnOwnAccountInAnotherCurrencyIsPairedByItsNumber(t *testing.T) {
	c := newClient(t)
	var euro bankAccount
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Eurový účet", "kind": "current", "currency": "EUR",
		"account_number": "000000-4455667788/2010"}, http.StatusCreated, &euro)
	c.upload("/api/v1/imports", "eur.csv", revolutEURStatement([]row{
		{"2026-10-01", "Kavárna Testova", "Predajca", -350},
	}), http.StatusCreated, nil)
	for _, a := range c.accounts() {
		if a.Currency == "EUR" && a.ID != euro.ID {
			c.do(http.MethodPost, "/api/v1/accounts/"+a.ID+"/merge", map[string]any{"target_id": euro.ID}, http.StatusOK, nil)
		}
	}

	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, []row{
		{"2026-10-12", "ODCHOZÍ PLATBA  EUROVY UCET TEST  000000 4455667788 2010", "Bez kategorie - jiné", -250000},
	}), http.StatusCreated, nil)
	payment := c.legs()["EUROVY UCET TEST"]
	require.NotNil(t, payment.Transfer, "until the other statement arrives the payment is a one-leg transfer")
	assert.Nil(t, payment.Transfer.CounterpartTxID)
	assert.Nil(t, payment.Transfer.CounterpartAmount)

	var arrival importResult
	c.upload("/api/v1/imports", "eur-next.csv", revolutEURStatement([]row{
		{"2026-10-01", "Kavárna Testova", "Predajca", -350},
		{"2026-10-13", "Platba z mBank", "Iné", 10000},
	}), http.StatusCreated, &arrival)
	assert.Equal(t, 1, arrival.TransfersMatched, "the payment names the euro account, so the amounts need not match")

	payment = c.transactionByID(payment.ID)
	require.NotNil(t, payment.Transfer)
	require.NotNil(t, payment.Transfer.CounterpartTxID)
	assert.Equal(t, euro.ID, payment.Transfer.CounterpartAccountID)
	require.NotNil(t, payment.Transfer.CounterpartAmount, "the list shows what arrived in euros")
	assert.Equal(t, int64(10000), payment.Transfer.CounterpartAmount.Minor)
	assert.Equal(t, "EUR", payment.Transfer.CounterpartAmount.Currency)
	arrivalLeg := *payment.Transfer.CounterpartTxID

	c.do(http.MethodDelete, "/api/v1/transfers/"+payment.ID, nil, http.StatusNoContent, nil)
	c.upload("/api/v1/imports", "mbank-next.csv", mbankStatement(t, ownMBankAccount, []row{
		{"2026-10-15", "PLATBA KARTOU  OBCHOD TEST", "Nákupy", -1000},
	}), http.StatusCreated, nil)
	assert.Nil(t, c.transactionByID(payment.ID).Transfer, "a pair the user undid stays undone")
	assert.Nil(t, c.transactionByID(arrivalLeg).Transfer)
}

func TestPaymentNamingAZeroPaddedOwnNumberPicksThatAccount(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "savings.csv", mbankStatement(t, "000000-9876543210/6210", []row{
		{"2026-10-13", "PŘÍCHOZÍ PLATBA  TEST KLIENT  000019 1234567890 6210", "Bez kategorie - jiné", 500000},
	}), http.StatusCreated, nil)
	c.upload("/api/v1/imports", "revolut.csv", revolutStatement(0, []row{
		{"2026-10-13", "Dobitie", "Dobiť účet", 500000},
	}), http.StatusCreated, nil)
	savings := c.legs()["TEST KLIENT"]

	var main importResult
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, []row{
		{"2026-10-12", "ODCHOZÍ PLATBA  SPORENI TEST  000000 9876543210 6210", "Bez kategorie - jiné", -500000},
	}), http.StatusCreated, &main)
	assert.Equal(t, 1, main.TransfersMatched, "the number written without leading zeros still names the savings account")

	payment := c.legs()["SPORENI TEST"]
	require.NotNil(t, payment.Transfer)
	require.NotNil(t, payment.Transfer.CounterpartTxID)
	assert.Equal(t, savings.ID, *payment.Transfer.CounterpartTxID)
}

func TestCounterpartsOfferAMovementInAnotherCurrencyThatAgreesAtTheRate(t *testing.T) {
	c := newClient(t)
	_, err := pg.Pool.Exec(t.Context(), `INSERT INTO fx_rate (day, currency, czk_per_unit, source) VALUES ('2026-10-01', 'EUR', $1, 'cnb')`,
		eurInCZK)
	require.NoError(t, err)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)
	var eur importResult
	c.upload("/api/v1/imports", "eur.csv", revolutEURStatement([]row{
		{"2026-10-13", "Dobitie", "Dobiť účet", 20000},
		{"2026-10-14", "Vrátenie platby", "Predajca", 350},
	}), http.StatusCreated, &eur)
	assert.Equal(t, 0, eur.TransfersMatched, "without an account number another currency waits for the user")

	topup := c.legs()["DOBITI REVOLUT"].ID
	var arrival string
	for _, tx := range c.transactions("account=" + *eur.Accounts[0].AccountID).Transactions {
		if tx.BookedOn == "2026-10-13" {
			arrival = tx.ID
		}
	}
	require.NotEmpty(t, arrival)
	var counterparts struct {
		Counterparts []struct {
			TransactionID string `json:"transaction_id"`
		} `json:"counterparts"`
	}
	c.do(http.MethodGet, "/api/v1/transfers/"+topup+"/counterparts", nil, http.StatusOK, &counterparts)
	require.Len(t, counterparts.Counterparts, 1, "the 3.50 € refund is far from 5 000 Kč at the rate")
	assert.Equal(t, arrival, counterparts.Counterparts[0].TransactionID)

	c.do(http.MethodPut, "/api/v1/transfers/"+topup+"/counterpart", map[string]string{"counterpart_tx_id": arrival},
		http.StatusOK, nil)
	assert.ElementsMatch(t, []string{topup, arrival}, transactionIDs(c.transactions("transfer=true")))
}

func TestALaterImportPairsAnOlderTransferOnceItsAccountIsKnown(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "other.csv", mbankStatement(t, "000019-5555555555/6210", []row{
		{"2026-10-13", "PŘÍCHOZÍ PLATBA  CIZI PLATCE  000000 1111111111 0100", "Bez kategorie - jiné", 500000},
	}), http.StatusCreated, nil)
	var revolut importResult
	c.upload("/api/v1/imports", "revolut.csv", revolutStatement(0, []row{
		{"2026-10-13", "Dobitie", "Dobiť účet", 500000},
	}), http.StatusCreated, &revolut)
	var first importResult
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, []row{
		{"2026-10-12", "ODCHOZÍ PLATBA  REVOLUT TEST  000000 9876543210 6210", "Bez kategorie - jiné", -500000},
	}), http.StatusCreated, &first)
	assert.Equal(t, 0, first.TransfersMatched, "two accounts received the sum and the number is nobody's yet")

	var withNumber bankAccount
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Revolut CZK", "kind": "current", "currency": "CZK",
		"account_number": "000000-9876543210/6210"}, http.StatusCreated, &withNumber)
	c.do(http.MethodPost, "/api/v1/accounts/"+*revolut.Accounts[0].AccountID+"/merge",
		map[string]any{"target_id": withNumber.ID}, http.StatusOK, nil)

	var next importResult
	c.upload("/api/v1/imports", "mbank-next.csv", mbankStatement(t, ownMBankAccount, []row{
		{"2026-10-15", "PLATBA KARTOU  OBCHOD TEST", "Nákupy", -1000},
	}), http.StatusCreated, &next)
	assert.Equal(t, 1, next.TransfersMatched, "the older payment now names a known account")
	payment := c.legs()["REVOLUT TEST"]
	require.NotNil(t, payment.Transfer)
	require.NotNil(t, payment.Transfer.CounterpartTxID)
	assert.Equal(t, withNumber.ID, payment.Transfer.CounterpartAccountID)
}
