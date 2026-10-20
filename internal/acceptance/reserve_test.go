//go:build integration

package acceptance

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// airBankMortgageExport is how Air Bank lists a mortgage with Chytrá rezerva: payments and reserve
// moves named by their operation type, with no counterparty account.
func airBankMortgageExport() []byte {
	header := `"Datum provedení","Typ úhrady","Měna účtu","Částka v měně účtu","Poplatek v měně účtu","Název protistrany",` +
		`"Číslo účtu protistrany","Variabilní symbol","Konstantní symbol","Specifický symbol","Zpráva pro příjemce",` +
		`"Poznámka k úhradě","Název karty","Datum zaúčtování","Referenční číslo","Kategorie plateb"`
	row := func(day, kind, amount, ref, category string) string {
		return `"` + day + `","` + kind + `","CZK","` + amount + `","","","","","","","","Hypotéka","","` + day + `","` + ref + `","` + category + `"`
	}
	return []byte(strings.Join([]string{header,
		row("15/01/2026", "Splátka hypotéky", "-16104,65", "300000001", "Splátky"),
		row("20/01/2026", "Úhrada do rezervy", "-600000,00", "300000002", "Splátky"),
		row("15/02/2026", "Splátka hypotéky", "-16104,65", "300000003", "Splátky"),
		row("01/03/2026", "Výběr z rezervy", "100000,00", "300000004", "Příchozí úhrada"),
		row("15/03/2026", "Splátka hypotéky", "-16104,65", "300000005", "Splátky"),
	}, "\n") + "\n")
}

func TestAirBankMortgageWithAReserveChargesInterestOnTheBalanceLessTheReserve(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "airbank_1234567890_2026-10-18_12-00.csv", airBankMortgageExport(), http.StatusCreated, nil)
	var mortgage bankAccount
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Hypotéka", "kind": "mortgage", "currency": "CZK",
		"bank_id": "cz-3030"}, http.StatusCreated, &mortgage)
	c.do(http.MethodPut, "/api/v1/loans/"+mortgage.ID+"/terms/2026-01-01", map[string]any{"balance_minor": 3_000_000_00,
		"annual_rate": "5", "payment_minor": 16_104_65, "payment_day": 15, "maturity_on": "2055-12-15"}, http.StatusNoContent, nil)

	type overview struct {
		Payments []struct {
			BookedOn string `json:"booked_on"`
			Interest amount `json:"interest"`
		} `json:"payments"`
		Reserve *struct {
			AccountID        string `json:"account_id"`
			Balance          amount `json:"balance"`
			Saved            amount `json:"saved"`
			Saves            amount `json:"saves"`
			PaidOffOn        string `json:"paid_off_on"`
			PaidOffWithoutOn string `json:"paid_off_without_on"`
		} `json:"reserve"`
	}
	var before overview
	c.do(http.MethodGet, "/api/v1/loans/"+mortgage.ID, nil, http.StatusOK, &before)
	require.Len(t, before.Payments, 3, "Air Bank's mortgage payments are recognised by their operation type")
	assert.Nil(t, before.Reserve)

	var reserve struct {
		AccountID string `json:"account_id"`
	}
	c.do(http.MethodPost, "/api/v1/loans/"+mortgage.ID+"/reserve", nil, http.StatusCreated, &reserve)
	c.do(http.MethodPost, "/api/v1/loans/"+mortgage.ID+"/reserve", nil, http.StatusConflict, nil)

	var after overview
	c.do(http.MethodGet, "/api/v1/loans/"+mortgage.ID, nil, http.StatusOK, &after)
	require.NotNil(t, after.Reserve)
	assert.Equal(t, reserve.AccountID, after.Reserve.AccountID)
	assert.Equal(t, int64(500_000_00), after.Reserve.Balance.Minor, "600 000 put in, 100 000 taken back")
	byDay := map[string]int64{}
	for _, p := range after.Payments {
		byDay[p.BookedOn] = p.Interest.Minor
	}
	feb := map[string]int64{}
	for _, p := range before.Payments {
		feb[p.BookedOn] = p.Interest.Minor
	}
	assert.Equal(t, feb["2026-01-15"], byDay["2026-01-15"], "nothing was in the reserve before 20 January")
	assert.Equal(t, feb["2026-02-15"]-2_500_00, byDay["2026-02-15"], "600 000 bears no interest: 2 500 a month less")
	assert.Positive(t, after.Reserve.Saved.Minor)
	assert.Positive(t, after.Reserve.Saves.Minor)
	assert.Less(t, after.Reserve.PaidOffOn, after.Reserve.PaidOffWithoutOn)

	var worth netWorth
	c.do(http.MethodGet, "/api/v1/reports/net-worth", nil, http.StatusOK, &worth)
	for _, a := range worth.Accounts {
		if a.AccountID == reserve.AccountID {
			assert.Equal(t, "flows", a.Valuation)
			assert.Equal(t, int64(500_000_00), a.Value.Minor, "the reserve is money at hand")
		}
	}

	stranger := c.signUp("stranger@coinbook.local")
	stranger.do(http.MethodPost, "/api/v1/loans/"+mortgage.ID+"/reserve", nil, http.StatusUnprocessableEntity, nil)
	stranger.do(http.MethodGet, "/api/v1/loans/"+mortgage.ID, nil, http.StatusUnprocessableEntity, nil)
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Rezerva", "kind": "loan_reserve", "currency": "CZK"},
		http.StatusUnprocessableEntity, nil)

	var report monthlyReport
	c.do(http.MethodGet, "/api/v1/reports/monthly/2026-01", nil, http.StatusOK, &report)
	require.Len(t, report.SetAside, 1)
	assert.Equal(t, reserve.AccountID, report.SetAside[0].AccountID)
	assert.Equal(t, int64(600_000_00), report.SetAside[0].AmountMinor)
}
