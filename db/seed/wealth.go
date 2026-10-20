package seed

import (
	"context"
	"errors"
	"time"

	"github.com/rotisserie/eris"
	shopspring "github.com/shopspring/decimal"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/importing"
	"coin-book-api/internal/domain/importing/parser/fingood"
	"coin-book-api/internal/domain/loan"
	"coin-book-api/internal/domain/networth"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/platform/xlsx"
)

// Account numbers the demo statement pays to, so the payments are recognised as transfers
// to the mortgage, the pension fund and Fingood (ADR 206, ADR 211).
const (
	demoMortgageAccount = "4444444444/0800"
	demoPensionAccount  = "7777777777/0300"
	demoFingoodAccount  = "2600000001/2010"
	demoReserveAccount  = "4444000001/0800"
)

var demoMortgage = loan.Terms{
	ValidFrom: time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC), BalanceMinor: 4_200_000_00, AnnualRate: shopspring.RequireFromString("4.79"),
	PaymentDay: 15, MaturityOn: time.Date(2053, 5, 15, 0, 0, 0, 0, time.UTC),
}

func init() {
	demoMortgage.PaymentMinor = loan.Annuity(demoMortgage.BalanceMinor, demoMortgage.AnnualRate, 360)
	fixed := time.Date(2028, 6, 1, 0, 0, 0, 0, time.UTC)
	demoMortgage.FixedUntil = &fixed
}

type demoAccounts struct {
	mortgage, pension, property, fingood *account.Account
}

// demoWealthAccounts creates, once, what the demo owns and owes besides its bank accounts.
func demoWealthAccounts(ctx context.Context, pool *db.Pool, accounts *account.Repo, loans *loan.Service) (*demoAccounts, error) {
	ensure := func(a *account.Account) (*account.Account, error) {
		existing, err := accounts.FindByIdentity(ctx, pool, a.AccountNumber, a.ExternalRef)
		if err != nil || existing != nil {
			return existing, err
		}
		return a, accounts.Create(ctx, pool, a)
	}
	number := func(s string) *string { return &s }
	propertyRef, fingoodRef := "demo:property", fingood.ExternalRef
	var out demoAccounts
	var err error
	if out.mortgage, err = ensure(&account.Account{Name: "Hypotéka", Institution: account.Other, Kind: account.Mortgage,
		Currency: money.CZK, AccountNumber: number(demoMortgageAccount)}); err != nil {
		return nil, err
	}
	// The mortgage has a reserve the statement pays into by number, as some banks' offset mortgages do.
	reserveID, err := loans.EnableReserve(ctx, pool, out.mortgage.ID)
	if err != nil && !errors.Is(err, loan.ErrReserveExists) {
		return nil, err
	}
	if _, err := pool.Exec(ctx, `UPDATE account SET account_number = $2 WHERE id = $1 AND account_number IS NULL`,
		reserveID, demoReserveAccount); err != nil {
		return nil, eris.Wrap(err, "number the demo reserve")
	}
	if out.pension, err = ensure(&account.Account{Name: "Penzijní spoření", Institution: account.Other, Kind: account.Pension,
		Currency: money.CZK, AccountNumber: number(demoPensionAccount)}); err != nil {
		return nil, err
	}
	if out.property, err = ensure(&account.Account{Name: "Byt Praha", Institution: account.Other, Kind: account.Property,
		Currency: money.CZK, ExternalRef: &propertyRef}); err != nil {
		return nil, err
	}
	if out.fingood, err = ensure(&account.Account{Name: "Fingood", Institution: account.Fingood, Kind: account.P2P,
		Currency: money.CZK, AccountNumber: number(demoFingoodAccount), ExternalRef: &fingoodRef}); err != nil {
		return nil, err
	}
	return &out, nil
}

// demoWealth gives the mortgage its terms, values the flat and the pension, and imports a
// Fingood export of today.
func demoWealth(ctx context.Context, pool *db.Pool, a *demoAccounts, loans *loan.Service, importer *importing.Service, today time.Time) error {
	if err := loans.SaveTerms(ctx, a.mortgage.ID, demoMortgage); err != nil {
		return err
	}
	values := networth.NewService(networth.NewRepo(pool), nil, loans, nil, clock.System{})
	start := today.AddDate(0, -demoMonths, 0)
	for _, v := range []networth.Valuation{
		{AccountID: a.property.ID, ValuedOn: demoMortgage.ValidFrom, ValueMinor: 6_900_000_00, Note: "kupní cena"},
		{AccountID: a.property.ID, ValuedOn: time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC), ValueMinor: 7_650_000_00, Note: "odhad"},
		{AccountID: a.pension.ID, ValuedOn: start, ValueMinor: 142_300_00},
		{AccountID: a.pension.ID, ValuedOn: today.AddDate(0, 0, -today.Day()), ValueMinor: 149_850_00},
	} {
		if err := values.SaveValuation(ctx, &v); err != nil {
			return err
		}
	}
	export, err := demoFingoodExport(today)
	if err != nil {
		return err
	}
	_, err = importer.Commit(ctx, importing.Upload{FileName: "fingood_moje_investice_export.xlsx", Content: export}, false)
	if err != nil && !errors.Is(err, importing.ErrAlreadyImported) {
		return err
	}
	return nil
}

// demoFingoodExport has the real export's columns and made-up loans: balloon and annuity loans
// being repaid, one late, one in preparation and one repaid.
func demoFingoodExport(today time.Time) ([]byte, error) {
	type n = xlsx.Number
	serial := func(t time.Time) n {
		return n(shopspring.NewFromInt(int64(t.Sub(time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)).Hours() / 24)).String())
	}
	in := func(months int) n { return serial(today.AddDate(0, months, 0)) }
	header := []any{"Číslo úvěru", "Název investice", "Způsob splácení", "Původní jistina", "Nakoupeno jistiny na Marketu",
		"Prodáno jistiny na Marketu", "Prodáno na Marketu za", "Nakoupeno na Marketu za", "Přislíbeno", "Úrok % (p.a.)",
		"Termín další splátky", "Termín poslední splátky", "Příští splátka", "Stav", "Stav splácení", "Investováno dne",
		"Zpoždění (dnů)", "Po splatnosti", "Sankční úroky", "Vráceno úroků", "Vráceno jistiny", "Poplatky",
		"Poplatky z prodeje na Marketu"}
	zero := n("0")
	return xlsx.Build([][]any{
		header,
		{"2300000101", "Rekonstrukce činžovního domu Brno", "Balonově", n("50000"), zero, zero, zero, zero, zero, n("9.5"),
			in(1), in(9), n("395.83"), "Splácení", "V pořádku", in(-11), nil, zero, zero, n("4354.13"), zero, zero, zero},
		{"2300000102", "Výstavba rodinných domů Říčany", "Balonově", n("40000"), zero, zero, zero, zero, zero, n("10.5"),
			in(1), in(17), n("350"), "Splácení", "V pořádku", in(-6), nil, zero, zero, n("2100"), zero, zero, zero},
		{"2300000103", "Nákup výrobní haly Olomouc", "Anuitně", n("30000"), zero, zero, zero, zero, zero, n("11"),
			in(1), in(26), n("980.5"), "Splácení", "V pořádku", in(-8), nil, zero, zero, n("1730.4"), n("6120"), zero, zero},
		{"2300000104", "Revitalizace areálu Kladno", "Balonově", n("25000"), zero, zero, zero, zero, zero, n("12"),
			in(0), in(5), n("250"), "Splácení", "Ve zpoždění", in(-14), n("21"), n("250"), n("8.6"), n("3250"), zero, zero, zero},
		{"2300000105", "Developerský projekt Plzeň", "Balonově", n("35000"), zero, zero, zero, zero, zero, n("10"),
			nil, nil, nil, "Příprava smluv", nil, serial(today.AddDate(0, 0, -3)), nil, nil, nil, nil, nil, nil, zero},
		{"2300000106", "Rekonstrukce penzionu Šumava", "Balonově", n("20000"), zero, zero, zero, zero, zero, n("9"),
			nil, nil, nil, "Splacen", nil, in(-20), nil, zero, zero, n("2700"), n("20000"), zero, zero},
	}, today.Add(18*time.Hour))
}
