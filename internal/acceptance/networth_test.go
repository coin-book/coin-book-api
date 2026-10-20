//go:build integration

package acceptance

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	shopspring "github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/domain/loan"
	"coin-book-api/internal/platform/xlsx"
)

type amount struct {
	Minor    int64  `json:"minor"`
	Currency string `json:"currency"`
}

type netWorth struct {
	Total       amount `json:"total_czk"`
	Assets      amount `json:"assets_czk"`
	Liabilities amount `json:"liabilities_czk"`
	OtherAssets amount `json:"other_assets_czk"`
	ByClass     []struct {
		Key   string `json:"key"`
		Value amount `json:"value"`
	} `json:"by_class"`
	Accounts []struct {
		AccountID string  `json:"account_id"`
		Class     string  `json:"class"`
		Valuation string  `json:"valuation"`
		Value     amount  `json:"value"`
		ValuedOn  *string `json:"valued_on"`
		Unvalued  bool    `json:"unvalued"`
	} `json:"accounts"`
}

func (w netWorth) class(key string) int64 {
	for _, c := range w.ByClass {
		if c.Key == key {
			return c.Value.Minor
		}
	}
	return 0
}

type n = xlsx.Number

var fingoodHeader = []any{"Číslo úvěru", "Název investice", "Způsob splácení", "Původní jistina", "Nakoupeno jistiny na Marketu",
	"Prodáno jistiny na Marketu", "Prodáno na Marketu za", "Nakoupeno na Marketu za", "Přislíbeno", "Úrok % (p.a.)",
	"Termín další splátky", "Termín poslední splátky", "Příští splátka", "Stav", "Stav splácení", "Investováno dne",
	"Zpoždění (dnů)", "Po splatnosti", "Sankční úroky", "Vráceno úroků", "Vráceno jistiny", "Poplatky",
	"Poplatky z prodeje na Marketu"}

// fingoodExport is a synthetic export of two loans: one repaid on time, one late.
func fingoodExport(t *testing.T, exported time.Time, interestPaid string) []byte {
	t.Helper()
	content, err := xlsx.Build([][]any{
		fingoodHeader,
		{"1000000001", "Rekonstrukce domu", "Balonově", n("50000"), n("0"), n("0"), n("0"), n("0"), n("0"), n("10"),
			n("46325"), n("46660"), n("416.67"), "Splácení", "V pořádku", n("45930"), nil, n("0"), n("0"), n(interestPaid), n("0"), n("0"), n("0")},
		{"1000000002", "Výstavba haly", "Anuitně", n("20000"), n("0"), n("0"), n("0"), n("0"), n("0"), n("12"),
			n("46301"), n("46900"), n("650"), "Splácení", "Ve zpoždění", n("45600"), n("17"), n("650"), n("10"), n("1000"), n("5000"), n("0"), n("0")},
	}, exported)
	require.NoError(t, err)
	return content
}

func TestAFingoodExportValuesThePlatformAtItsOutstandingPrincipal(t *testing.T) {
	c := newClient(t)
	var first importResult
	c.upload("/api/v1/imports", "fingood_moje_investice_export.xlsx",
		fingoodExport(t, time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC), "1000"), http.StatusCreated, &first)
	require.Len(t, first.Accounts, 1)
	assert.Equal(t, 2, first.RowsNew)
	require.NotNil(t, first.Accounts[0].AccountID)
	accountID := *first.Accounts[0].AccountID

	var worth netWorth
	c.do(http.MethodGet, "/api/v1/reports/net-worth", nil, http.StatusOK, &worth)
	assert.Equal(t, int64(65_000_00), worth.class("p2p"), "50 000 plus 20 000 less 5 000 repaid")
	assert.Equal(t, int64(65_000_00), worth.OtherAssets.Minor)

	c.upload("/api/v1/imports", "fingood_moje_investice_export (1).xlsx",
		fingoodExport(t, time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC), "1416.67"), http.StatusCreated, nil)
	var snapshot struct {
		On                  string  `json:"on"`
		Outstanding         amount  `json:"outstanding"`
		AtRisk              amount  `json:"at_risk"`
		Earned              amount  `json:"earned"`
		EarnedSincePrevious *amount `json:"earned_since_previous"`
		PreviousOn          *string `json:"previous_on"`
		AverageRate         string  `json:"average_rate"`
		Maturities          []struct {
			Month       *string `json:"month"`
			Outstanding amount  `json:"outstanding"`
		} `json:"maturities"`
		History []struct{ On string } `json:"history"`
		Loans   []struct {
			LoanNumber string `json:"loan_number"`
			AtRisk     bool   `json:"at_risk"`
		} `json:"loans"`
	}
	c.do(http.MethodGet, "/api/v1/accounts/"+accountID+"/p2p-loans", nil, http.StatusOK, &snapshot)
	assert.Equal(t, "2026-10-15", snapshot.On)
	assert.Equal(t, int64(15_000_00), snapshot.AtRisk.Minor, "the late loan's outstanding principal")
	require.NotNil(t, snapshot.EarnedSincePrevious)
	assert.Equal(t, int64(416_67), snapshot.EarnedSincePrevious.Minor)
	assert.Equal(t, "2026-09-30", *snapshot.PreviousOn)
	assert.Equal(t, int64(1_416_67+1_000_00+10_00), snapshot.Earned.Minor, "interest and penalties of both loans")
	assert.Equal(t, "10.46", snapshot.AverageRate, "weighted by 50 000 at 10 % and 15 000 at 12 %")
	require.Len(t, snapshot.Maturities, 2)
	assert.Equal(t, "2027-09", *snapshot.Maturities[0].Month)
	assert.Len(t, snapshot.History, 2)

	c.do(http.MethodPost, "/api/v1/imports/"+first.BatchID+"/rollback", nil, http.StatusOK, nil)
	var remaining struct {
		EarnedSincePrevious *amount               `json:"earned_since_previous"`
		History             []struct{ On string } `json:"history"`
	}
	c.do(http.MethodGet, "/api/v1/accounts/"+accountID+"/p2p-loans", nil, http.StatusOK, &remaining)
	assert.Len(t, remaining.History, 1, "the rolled-back export's snapshot is gone")
	assert.Nil(t, remaining.EarnedSincePrevious)
}

func TestAssetsValuedByHandAndTheirReturn(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, []row{
		{"2026-01-10", "ODCHOZÍ PLATBA  PENZIJNI SPOLECNOST  000000 7777777777 0300", "Bez kategorie - jiné", -12_000_00},
		{"2026-07-10", "ODCHOZÍ PLATBA  PENZIJNI SPOLECNOST  000000 7777777777 0300", "Bez kategorie - jiné", -12_000_00},
	}), http.StatusCreated, nil)
	var pension, flat, current bankAccount
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Penzijko", "kind": "pension", "currency": "CZK",
		"account_number": "7777777777/0300"}, http.StatusCreated, &pension)
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Byt", "kind": "property", "currency": "CZK"}, http.StatusCreated, &flat)
	for _, a := range c.bankAccounts() {
		if a.ID != pension.ID && a.ID != flat.ID {
			current = a
		}
	}

	var worth netWorth
	c.do(http.MethodGet, "/api/v1/reports/net-worth", nil, http.StatusOK, &worth)
	for _, a := range worth.Accounts {
		if a.AccountID == flat.ID {
			assert.True(t, a.Unvalued, "a property is worth nothing until valued")
		}
	}

	c.do(http.MethodPost, "/api/v1/accounts/"+flat.ID+"/valuations", map[string]any{"valued_on": "2026-06-30", "value_minor": 6_500_000_00,
		"note": "odhad"}, http.StatusOK, nil)
	c.do(http.MethodPost, "/api/v1/accounts/"+pension.ID+"/valuations", map[string]any{"valued_on": "2026-10-01", "value_minor": 25_000_00},
		http.StatusOK, nil)
	c.do(http.MethodPost, "/api/v1/accounts/"+current.ID+"/valuations", map[string]any{"valued_on": "2026-10-01", "value_minor": 1},
		http.StatusUnprocessableEntity, nil)

	c.do(http.MethodGet, "/api/v1/reports/net-worth", nil, http.StatusOK, &worth)
	assert.Equal(t, int64(6_500_000_00), worth.class("property"))
	assert.Equal(t, int64(25_000_00), worth.class("pension"))

	var performance struct {
		Contributed  amount  `json:"contributed"`
		Gain         *amount `json:"gain"`
		AnnualReturn *string `json:"annual_return"`
		Flows        []struct{ On string }
	}
	c.do(http.MethodGet, "/api/v1/accounts/"+pension.ID+"/performance", nil, http.StatusOK, &performance)
	assert.Equal(t, int64(24_000_00), performance.Contributed.Minor, "both payments from the current account")
	require.NotNil(t, performance.Gain)
	assert.Equal(t, int64(1_000_00), performance.Gain.Minor)
	require.NotNil(t, performance.AnnualReturn)
	assert.True(t, shopspring.RequireFromString(*performance.AnnualReturn).IsPositive())

	var history struct {
		Months []struct {
			Month string `json:"month"`
			Total amount `json:"total_czk"`
			Saved amount `json:"saved_czk"`
		} `json:"months"`
	}
	c.do(http.MethodGet, "/api/v1/reports/net-worth/history?months=6", nil, http.StatusOK, &history)
	require.Len(t, history.Months, 6)
	assert.Equal(t, "2026-05", history.Months[0].Month)
	assert.Equal(t, "2026-10", history.Months[5].Month)
	assert.Less(t, history.Months[0].Total.Minor, history.Months[1].Total.Minor, "the flat is valued from June's end")

	stranger := c.signUp("stranger@coinbook.local")
	stranger.do(http.MethodPost, "/api/v1/accounts/"+flat.ID+"/valuations", map[string]any{"valued_on": "2026-10-01", "value_minor": 1},
		http.StatusNotFound, nil)
	stranger.do(http.MethodGet, "/api/v1/accounts/"+flat.ID+"/valuations", nil, http.StatusNotFound, nil)
}

const mortgageBankAccount = "5555555555/0800"

var mortgageTerms = map[string]any{"balance_minor": 3_000_000_00, "annual_rate": "5", "payment_minor": 16_104_65,
	"payment_day": 15, "maturity_on": "2055-12-15", "fixed_until": "2031-01-01"}

func mortgageRows() []row {
	return []row{
		{"2026-09-15", "ODCHOZÍ PLATBA  SPLATKA HYPOTEKY  000000 5555555555 0800", "Bez kategorie - jiné", -16_104_65},
		{"2026-10-15", "ODCHOZÍ PLATBA  SPLATKA HYPOTEKY  000000 5555555555 0800", "Bez kategorie - jiné", -16_104_65},
	}
}

func TestMortgagePaymentsSplitIntoInterestAndPrincipal(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, mortgageRows()), http.StatusCreated, nil)
	var mortgage bankAccount
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Hypotéka", "kind": "mortgage", "currency": "CZK",
		"account_number": mortgageBankAccount}, http.StatusCreated, &mortgage)
	assert.NotNil(t, c.legs()["SPLATKA HYPOTEKY"].Transfer, "without terms a payment to the loan is a plain transfer")

	c.do(http.MethodPut, "/api/v1/loans/"+mortgage.ID+"/terms/2026-01-01", mortgageTerms, http.StatusNoContent, nil)

	expected := loan.Build([]loan.Terms{{ValidFrom: day(2026, 1, 1), BalanceMinor: 3_000_000_00, AnnualRate: shopspring.NewFromInt(5),
		PaymentMinor: 16_104_65, PaymentDay: 15, MaturityOn: day(2055, 12, 15)}}, nil, []loan.Payment{
		{TransactionID: "sep", BookedOn: day(2026, 9, 15), AmountMinor: 16_104_65},
		{TransactionID: "oct", BookedOn: day(2026, 10, 15), AmountMinor: 16_104_65},
	}, nil, day(2026, 10, 16))

	var overview struct {
		Balance amount `json:"balance"`
		Current *struct {
			FixedUntil string `json:"fixed_until"`
		} `json:"current"`
		Next *struct {
			DueOn string `json:"due_on"`
		} `json:"next"`
		Payments []struct {
			TransactionID string `json:"transaction_id"`
			BookedOn      string `json:"booked_on"`
			Interest      amount `json:"interest"`
			Principal     amount `json:"principal"`
			BySchedule    bool   `json:"by_schedule"`
		} `json:"payments"`
		Years []struct {
			Year       int    `json:"year"`
			Interest   amount `json:"interest"`
			Deductible amount `json:"deductible"`
		} `json:"years"`
	}
	c.do(http.MethodGet, "/api/v1/loans/"+mortgage.ID, nil, http.StatusOK, &overview)
	require.Len(t, overview.Payments, 2, "the transfers to the loan became its instalments")
	october := overview.Payments[0]
	assert.Equal(t, "2026-10-15", october.BookedOn)
	assert.Equal(t, expected.Splits["oct"].InterestMinor, october.Interest.Minor)
	assert.Equal(t, int64(16_104_65), october.Interest.Minor+october.Principal.Minor)
	assert.True(t, october.BySchedule)
	assert.Equal(t, expected.BalanceOn(day(2026, 10, 16)), overview.Balance.Minor)
	assert.Equal(t, "2031-01-01", overview.Current.FixedUntil)
	assert.Equal(t, "2026-11-15", overview.Next.DueOn)
	require.Len(t, overview.Years, 1)
	assert.Equal(t, expected.InterestByYear()[2026], overview.Years[0].Interest.Minor)
	assert.Equal(t, overview.Years[0].Interest.Minor, overview.Years[0].Deductible.Minor, "under the 150 000 Kč limit")

	assert.Nil(t, c.legs()["SPLATKA HYPOTEKY"].Transfer, "an instalment is no longer a transfer")
	var paid struct {
		Categories []struct {
			Source string `json:"source"`
		} `json:"categories"`
		LoanPayment *struct {
			AccountID     string `json:"account_id"`
			InterestMinor int64  `json:"interest_minor"`
		} `json:"loan_payment"`
	}
	c.do(http.MethodGet, "/api/v1/transactions/"+october.TransactionID, nil, http.StatusOK, &paid)
	require.NotNil(t, paid.LoanPayment)
	assert.Equal(t, mortgage.ID, paid.LoanPayment.AccountID)
	assert.Equal(t, october.Interest.Minor, paid.LoanPayment.InterestMinor)
	require.Len(t, paid.Categories, 2)
	for _, c := range paid.Categories {
		assert.Equal(t, "loan", c.Source, "the classifier must not learn from a split the schedule made")
	}
	var report struct {
		ExpenseMinor   int64 `json:"expense_minor"`
		FinancialMinor int64 `json:"financial_minor"`
	}
	c.do(http.MethodGet, "/api/v1/reports/monthly/2026-10", nil, http.StatusOK, &report)
	assert.Equal(t, october.Interest.Minor, report.ExpenseMinor, "only the interest is spent")
	assert.Equal(t, october.Principal.Minor, report.FinancialMinor, "the principal is repaid")

	var worth netWorth
	c.do(http.MethodGet, "/api/v1/reports/net-worth", nil, http.StatusOK, &worth)
	assert.Equal(t, -overview.Balance.Minor, worth.class("mortgage"))
	assert.Equal(t, overview.Balance.Minor+2*16_104_65, worth.Liabilities.Minor, "the current account, overdrawn by the instalments, is owed too")

	var prepayment struct {
		Current     struct{ Months int } `json:"current"`
		KeepPayment struct {
			Months   int    `json:"months"`
			Interest amount `json:"interest"`
		} `json:"keep_payment"`
	}
	c.do(http.MethodGet, "/api/v1/loans/"+mortgage.ID+"/prepayment?extra_minor=50000000", nil, http.StatusOK, &prepayment)
	assert.Less(t, prepayment.KeepPayment.Months, prepayment.Current.Months)

	c.do(http.MethodPut, "/api/v1/transactions/"+october.TransactionID+"/loan-payment", map[string]any{"account_id": mortgage.ID,
		"interest_minor": 12_000_00, "principal_minor": 4_000_00}, http.StatusUnprocessableEntity, nil)
	c.do(http.MethodPut, "/api/v1/transactions/"+october.TransactionID+"/loan-payment", map[string]any{"account_id": mortgage.ID,
		"interest_minor": 12_000_00, "principal_minor": 4_104_65}, http.StatusNoContent, nil)
	c.do(http.MethodGet, "/api/v1/reports/monthly/2026-10", nil, http.StatusOK, &report)
	assert.Equal(t, int64(12_000_00), report.ExpenseMinor, "the user's own split")

	c.do(http.MethodDelete, "/api/v1/transactions/"+october.TransactionID+"/loan-payment", nil, http.StatusNoContent, nil)
	c.upload("/api/v1/imports", "next.csv", mbankStatement(t, ownMBankAccount, []row{
		{"2026-10-16", "PLATBA KARTOU  OBCHOD TEST", "Nákupy", -1000},
	}), http.StatusCreated, nil)
	c.do(http.MethodGet, "/api/v1/loans/"+mortgage.ID, nil, http.StatusOK, &overview)
	assert.Len(t, overview.Payments, 1, "a payment the user unmarked does not come back")

	stranger := c.signUp("stranger@coinbook.local")
	stranger.do(http.MethodGet, "/api/v1/loans/"+mortgage.ID, nil, http.StatusUnprocessableEntity, nil)
	stranger.do(http.MethodPut, "/api/v1/loans/"+mortgage.ID+"/terms/2026-01-01", mortgageTerms, http.StatusUnprocessableEntity, nil)
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestLoanPaymentsKeepTheUsersCategoriesAndSurviveAMerge(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, mortgageRows()), http.StatusCreated, nil)
	var mortgage, duplicate bankAccount
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Hypotéka", "kind": "mortgage", "currency": "CZK",
		"account_number": mortgageBankAccount}, http.StatusCreated, &mortgage)
	c.do(http.MethodPut, "/api/v1/loans/"+mortgage.ID+"/terms/2026-01-01", mortgageTerms, http.StatusNoContent, nil)

	var overview struct {
		Balance  amount `json:"balance"`
		Payments []struct {
			TransactionID string `json:"transaction_id"`
		} `json:"payments"`
	}
	c.do(http.MethodGet, "/api/v1/loans/"+mortgage.ID, nil, http.StatusOK, &overview)
	require.Len(t, overview.Payments, 2)
	paid := overview.Payments[0].TransactionID

	c.do(http.MethodPut, "/api/v1/transactions/"+paid+"/categories", map[string]any{"lines": []map[string]any{
		{"category_id": c.categoryID("services.bank_fees"), "amount_minor": -16_104_65}}}, http.StatusOK, nil)
	c.do(http.MethodPut, "/api/v1/loans/"+mortgage.ID+"/terms/2026-01-01", mortgageTerms, http.StatusNoContent, nil)
	var tx struct {
		Categories []struct {
			Source string `json:"source"`
		} `json:"categories"`
	}
	c.do(http.MethodGet, "/api/v1/transactions/"+paid, nil, http.StatusOK, &tx)
	require.Len(t, tx.Categories, 1)
	assert.Equal(t, "manual", tx.Categories[0].Source, "splitting again leaves the user's own category")

	c.do(http.MethodPut, "/api/v1/transfers/"+paid+"/own", map[string]any{"account_id": mortgage.ID}, http.StatusUnprocessableEntity, nil)
	c.do(http.MethodGet, "/api/v1/loans/"+mortgage.ID+"/prepayment?extra_minor="+strconv.FormatInt(overview.Balance.Minor, 10), nil,
		http.StatusUnprocessableEntity, nil)

	var current bankAccount
	for _, a := range c.bankAccounts() {
		if a.ID != mortgage.ID {
			current = a
		}
	}
	c.do(http.MethodPost, "/api/v1/accounts/"+mortgage.ID+"/merge", map[string]any{"target_id": current.ID},
		http.StatusUnprocessableEntity, nil)
	c.do(http.MethodPatch, "/api/v1/accounts/"+mortgage.ID, map[string]any{"kind": "current"}, http.StatusUnprocessableEntity, nil)
	c.do(http.MethodPatch, "/api/v1/accounts/"+mortgage.ID, map[string]any{"kind": "loan"}, http.StatusOK, nil)
	c.do(http.MethodPatch, "/api/v1/accounts/"+mortgage.ID, map[string]any{"kind": "mortgage"}, http.StatusOK, nil)
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Hypotéka 2", "kind": "mortgage", "currency": "CZK"},
		http.StatusCreated, &duplicate)
	c.do(http.MethodPost, "/api/v1/accounts/"+mortgage.ID+"/merge", map[string]any{"target_id": duplicate.ID}, http.StatusOK, nil)
	c.do(http.MethodGet, "/api/v1/loans/"+duplicate.ID, nil, http.StatusOK, &overview)
	assert.Len(t, overview.Payments, 2, "terms and payments move with the merge")
	assert.Positive(t, overview.Balance.Minor)
}

func TestAPaymentCategorisedBeforeTheLoanExistedIsSplitOnceAdopted(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, mortgageRows()), http.StatusCreated, nil)
	paid := c.legs()["SPLATKA HYPOTEKY"].ID
	c.do(http.MethodPut, "/api/v1/transactions/"+paid+"/categories", map[string]any{"lines": []map[string]any{
		{"category_id": c.categoryID("housing.rent"), "amount_minor": -16_104_65}}}, http.StatusOK, nil)

	var mortgage bankAccount
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Hypotéka", "kind": "mortgage", "currency": "CZK",
		"account_number": mortgageBankAccount}, http.StatusCreated, &mortgage)
	c.do(http.MethodPut, "/api/v1/loans/"+mortgage.ID+"/terms/2026-01-01", mortgageTerms, http.StatusNoContent, nil)

	var tx struct {
		Categories []struct {
			Source string `json:"source"`
		} `json:"categories"`
	}
	c.do(http.MethodGet, "/api/v1/transactions/"+paid, nil, http.StatusOK, &tx)
	require.Len(t, tx.Categories, 2, "interest and principal replace the category given by hand before")
	for _, cat := range tx.Categories {
		assert.Equal(t, "loan", cat.Source)
	}
}
