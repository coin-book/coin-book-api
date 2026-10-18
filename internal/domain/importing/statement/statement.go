// Package statement is the normalised form every bank parser produces.
package statement

import (
	"io"
	"time"

	"github.com/shopspring/decimal"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/platform/money"
)

type Format string

type Confidence int

const (
	NoMatch Confidence = 0
	Likely  Confidence = 50
	Certain Confidence = 100
)

type Parser interface {
	Format() Format
	Version() int
	Detect(head []byte) Confidence
	Parse(r io.Reader) (*Statement, error)
}

type Statement struct {
	Format     Format
	Version    int
	PeriodFrom *time.Time
	PeriodTo   *time.Time
	Accounts   []*Account
}

type AccountRef struct {
	Institution   account.Institution
	Kind          account.Kind
	Currency      money.Currency
	AccountNumber *string
	ExternalRef   *string
	Name          string
}

// Identity matches account.Account.Identity, so a statement account and a stored
// one derive the same transaction ids. Empty means the file does not say which
// account it is, and the user has to map it.
func (r AccountRef) Identity() string {
	switch {
	case r.AccountNumber != nil:
		return *r.AccountNumber
	case r.ExternalRef != nil:
		return *r.ExternalRef
	default:
		return ""
	}
}

type Account struct {
	Ref  AccountRef
	Rows []Row
	// Totals is the statement's own income/expense summary; nil when the file has none.
	Totals *Totals
	// DeclaredSum is the statement's sum of all movements; nil when the file has none.
	DeclaredSum *int64
	// Investments is set for a broker account (ADR 209).
	Investments *Investments `json:",omitempty"`
}

// HasRecords tells whether the account carries anything to import.
func (a *Account) HasRecords() bool {
	return len(a.Rows) > 0 || (a.Investments != nil && a.Investments.Len() > 0)
}

type Totals struct {
	IncomeMinor  int64
	ExpenseMinor int64
}

type Row struct {
	Line                int
	BookedOn            time.Time
	AmountMinor         int64
	FeeMinor            int64
	Description         string
	Counterparty        string
	CounterpartyAccount string
	OperationType       string
	Message             string
	VariableSymbol      string
	ConstantSymbol      string
	SpecificSymbol      string
	BankCategory        string
	BalanceAfterMinor   *int64
	// CategoryHint is a category code or name the file itself assigns; empty when it has none.
	CategoryHint string
	// IdentityKey is what the transaction id is hashed from, together with the account
	// and the row's occurrence among identical keys. Fixed per parser version.
	IdentityKey string
}

type Investments struct {
	Buys      []Buy
	Dividends []Dividend
	LotSales  []LotSale
}

func (i *Investments) Len() int { return len(i.Buys) + len(i.Dividends) + len(i.LotSales) }

// Security is an instrument as a statement names it; Ticker and Name may be empty.
type Security struct {
	ISIN   string
	Ticker string
	Name   string
}

// Fees and taxes are in CZK: a statement may charge them in another currency than the trade's.
type Buy struct {
	Line          int
	TradedOn      time.Time
	Security      Security
	Quantity      decimal.Decimal
	Currency      money.Currency
	GrossMinor    int64
	NetMinor      int64
	GrossCZKMinor *int64
	NetCZKMinor   *int64
	FeeCZKMinor   int64
	TaxCZKMinor   int64
	IdentityKey   string
}

type Dividend struct {
	Line          int
	PaidOn        time.Time
	Security      Security
	Currency      money.Currency
	GrossMinor    int64
	NetMinor      int64
	GrossCZKMinor *int64
	NetCZKMinor   *int64
	FeeCZKMinor   int64
	TaxCZKMinor   int64
	IdentityKey   string
}

// LotSale is one sold lot as the broker matched it to its purchase. Cost is positive.
type LotSale struct {
	Line             int
	SoldOn           time.Time
	BoughtOn         time.Time
	Security         Security
	Quantity         decimal.Decimal
	Currency         money.Currency
	ProceedsMinor    int64
	CostMinor        int64
	GainMinor        int64
	ProceedsCZKMinor *int64
	CostCZKMinor     *int64
	GainCZKMinor     *int64
	FeeCZKMinor      int64
	TaxCZKMinor      int64
	IdentityKey      string
}
