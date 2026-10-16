// Package statement is the normalised form every bank parser produces.
package statement

import (
	"io"
	"time"

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
// one derive the same transaction ids.
func (r AccountRef) Identity() string {
	if r.AccountNumber != nil {
		return *r.AccountNumber
	}
	return *r.ExternalRef
}

type Account struct {
	Ref  AccountRef
	Rows []Row
	// Totals is the statement's own income/expense summary; nil when the file has none.
	Totals *Totals
	// DeclaredSum is the statement's sum of all movements; nil when the file has none.
	DeclaredSum *int64
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
	// IdentityKey is what the transaction id is hashed from, together with the account
	// and the row's occurrence among identical keys. Fixed per parser version.
	IdentityKey string
}
