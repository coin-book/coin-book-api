// Package statement is the normalised form every bank parser produces.
package statement

import (
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	// OpeningBalanceMinor is the balance before the first row; nil when the file has none.
	OpeningBalanceMinor *int64 `json:",omitempty"`
	// Investments is set for a broker account (ADR 209).
	Investments *Investments `json:",omitempty"`
	// P2P is set for a P2P platform's export of its loans (ADR 211).
	P2P *P2PSnapshot `json:",omitempty"`
}

// HasRecords tells whether the account carries anything to import.
func (a *Account) HasRecords() bool {
	return len(a.Rows) > 0 || (a.Investments != nil && a.Investments.Len() > 0) || (a.P2P != nil && len(a.P2P.Positions) > 0)
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

// Security is an instrument as a statement names it; Ticker, Name and YahooSymbol may be empty.
type Security struct {
	ISIN   string
	Ticker string
	Name   string
	// YahooSymbol is the statement's own listing (CSPX.L), better than a guess from the currency.
	YahooSymbol string `json:",omitempty"`
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

// P2PSnapshot is a platform's loans as its export showed them on one day.
type P2PSnapshot struct {
	TakenOn   time.Time
	Positions []P2PPosition
}

// P2PPosition is one loan; the returned, paid and fee amounts are cumulative since investing.
type P2PPosition struct {
	Line                   int
	LoanNumber             string
	Name                   string
	Repayment              string
	Status                 string
	RepaymentStatus        string
	InvestedMinor          int64
	OutstandingMinor       int64
	InterestRate           decimal.Decimal
	InvestedOn             *time.Time
	NextPaymentOn          *time.Time
	NextPaymentMinor       *int64
	FinalPaymentOn         *time.Time
	DelayDays              int
	OverdueMinor           int64
	InterestPaidMinor      int64
	PenaltyInterestMinor   int64
	PrincipalReturnedMinor int64
	MarketGainMinor        int64
	FeesMinor              int64
}

var (
	isinPattern   = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{9}[0-9]$`)
	tickerPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.\-]{0,11}$`)
	// A listed instrument: indices (^GSPC) and currency or futures pairs (=X, =F) are not one.
	yahooPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.\-]{0,19}$`)
)

const maxNameLength = 120

// ValidISIN checks the shape and the ISO 6166 check digit: letters become 10–35, then Luhn.
func ValidISIN(isin string) bool {
	if !isinPattern.MatchString(isin) {
		return false
	}
	var digits strings.Builder
	for _, c := range isin {
		if c >= 'A' && c <= 'Z' {
			digits.WriteString(strconv.Itoa(int(c-'A') + 10))
		} else {
			digits.WriteRune(c)
		}
	}
	s := digits.String()
	sum := 0
	for i := len(s) - 1; i >= 0; i-- {
		d := int(s[i] - '0')
		if (len(s)-1-i)%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

// NewSecurity keeps what a statement says about an instrument only when it looks like a ticker,
// a name and a Yahoo symbol: instruments are shared, and their text is shown to other holders.
func NewSecurity(isin, ticker, name, yahooSymbol string) Security {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	if !tickerPattern.MatchString(ticker) {
		ticker = ""
	}
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxNameLength {
		name = string([]rune(name)[:maxNameLength])
	}
	if !yahooPattern.MatchString(yahooSymbol) {
		yahooSymbol = ""
	}
	return Security{ISIN: isin, Ticker: ticker, Name: name, YahooSymbol: yahooSymbol}
}
