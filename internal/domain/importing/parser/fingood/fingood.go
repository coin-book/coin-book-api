// Package fingood parses Fingood's "Moje investice" XLSX export: the investor's loans on the
// day of the export, not their movements (coin-book/importer-formaty.md, section Fingood).
package fingood

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rotisserie/eris"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/platform/xlsx"
)

const Format statement.Format = "fingood-investments"

const version = 1

// ExternalRef is the one Fingood account a user has; the export names no account.
const ExternalRef = "fingood:investments"

const (
	columnLoan              = "Číslo úvěru"
	columnName              = "Název investice"
	columnRepayment         = "Způsob splácení"
	columnPrincipal         = "Původní jistina"
	columnMarketBought      = "Nakoupeno jistiny na Marketu"
	columnMarketSold        = "Prodáno jistiny na Marketu"
	columnMarketSoldFor     = "Prodáno na Marketu za"
	columnMarketBoughtFor   = "Nakoupeno na Marketu za"
	columnRate              = "Úrok % (p.a.)"
	columnNextPaymentOn     = "Termín další splátky"
	columnFinalPaymentOn    = "Termín poslední splátky"
	columnNextPayment       = "Příští splátka"
	columnStatus            = "Stav"
	columnRepaymentStatus   = "Stav splácení"
	columnInvestedOn        = "Investováno dne"
	columnDelay             = "Zpoždění (dnů)"
	columnOverdue           = "Po splatnosti"
	columnPenaltyInterest   = "Sankční úroky"
	columnInterestPaid      = "Vráceno úroků"
	columnPrincipalReturned = "Vráceno jistiny"
	columnFees              = "Poplatky"
	columnMarketFees        = "Poplatky z prodeje na Marketu"
)

// requiredColumns identify the export and carry the value of a loan.
var requiredColumns = []string{columnLoan, columnName, columnPrincipal, columnPrincipalReturned, columnStatus}

// Lengths the database allows; longer texts are cut rather than refused.
const (
	maxLoanNumber = 40
	maxName       = 200
	maxRepayment  = 40
	maxLabel      = 60
)

// Plain numbers only: decimal parsing takes exponents such as 1e-2000000000, whose scaling costs
// minutes of CPU and gigabytes; the bounds also keep amounts within int64 and days within int.
// Apache POI writes doubles at full precision (12.340000000000002), hence the long fractions.
var (
	plainAmount = regexp.MustCompile(`^-?[0-9]{1,11}(\.[0-9]{1,17})?$`)
	plainRate   = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,17})?$`)
	plainDays   = regexp.MustCompile(`^-?[0-9]{1,6}(\.0+)?$`)
)

var maxRate = decimal.NewFromInt(1000)

var (
	zipMagic         = []byte("PK\x03\x04")
	ErrMissingColumn = eris.New("fingood: a required column is missing")
	ErrNoExportDate  = eris.New("fingood: the file does not say when it was exported")
	ErrNoPositions   = eris.New("fingood: the export lists no loans")
	ErrInvalidValue  = eris.New("fingood: a cell holds no valid value")
	ErrDuplicateLoan = eris.New("fingood: a loan is listed twice")
)

type Parser struct{}

func (Parser) Format() statement.Format { return Format }

func (Parser) Version() int { return version }

// Detect sees only a compressed head, so it can only tell a workbook; Parse checks the columns.
func (Parser) Detect(head []byte) statement.Confidence {
	if bytes.HasPrefix(head, zipMagic) && bytes.Contains(head, []byte("xl/")) {
		return statement.Likely
	}
	return statement.NoMatch
}

func (Parser) Parse(r io.Reader) (*statement.Statement, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return nil, eris.Wrap(err, "fingood: read")
	}
	wb, err := xlsx.Read(content)
	if err != nil {
		return nil, err
	}
	if len(wb.Rows) == 0 {
		return nil, ErrNoPositions
	}
	columns := map[string]int{}
	for i, name := range wb.Rows[0] {
		columns[strings.TrimSpace(name)] = i
	}
	for _, name := range requiredColumns {
		if _, ok := columns[name]; !ok {
			return nil, eris.Wrapf(ErrMissingColumn, "%s", name)
		}
	}
	if wb.Created.IsZero() {
		return nil, ErrNoExportDate
	}
	created := wb.Created.In(clock.BusinessZone)
	takenOn := time.Date(created.Year(), created.Month(), created.Day(), 0, 0, 0, 0, time.UTC)

	snapshot := &statement.P2PSnapshot{TakenOn: takenOn}
	seen := map[string]bool{}
	for i, cells := range wb.Rows[1:] {
		row := sheetRow{wb: wb, columns: columns, cells: cells, line: i + 2}
		if row.text(columnLoan) == "" {
			continue
		}
		p, err := row.position()
		if err != nil {
			return nil, err
		}
		if seen[p.LoanNumber] {
			return nil, eris.Wrapf(ErrDuplicateLoan, "line %d, %s", row.line, p.LoanNumber)
		}
		seen[p.LoanNumber] = true
		snapshot.Positions = append(snapshot.Positions, p)
	}
	if len(snapshot.Positions) == 0 {
		return nil, ErrNoPositions
	}
	externalRef := ExternalRef
	return &statement.Statement{
		Format: Format, Version: version, PeriodFrom: &takenOn, PeriodTo: &takenOn,
		Accounts: []*statement.Account{{
			Ref: statement.AccountRef{Institution: account.Fingood, Kind: account.P2P, Currency: money.CZK,
				ExternalRef: &externalRef, Name: "Fingood"},
			P2P: snapshot,
		}},
	}, nil
}

type sheetRow struct {
	wb      *xlsx.Workbook
	columns map[string]int
	cells   []string
	line    int
}

func (r sheetRow) text(column string) string {
	i, ok := r.columns[column]
	if !ok || i >= len(r.cells) {
		return ""
	}
	return strings.TrimSpace(r.cells[i])
}

func (r sheetRow) invalid(column string) error {
	return eris.Wrapf(ErrInvalidValue, "line %d, %s", r.line, column)
}

// amount reads a number in crowns into hellers; an empty cell is zero.
func (r sheetRow) amount(column string) (int64, error) {
	text := r.text(column)
	if text == "" {
		return 0, nil
	}
	if !plainAmount.MatchString(text) {
		return 0, r.invalid(column)
	}
	d, err := decimal.NewFromString(text)
	if err != nil {
		return 0, r.invalid(column)
	}
	return d.Shift(2).Round(0).IntPart(), nil
}

func (r sheetRow) optionalAmount(column string) (*int64, error) {
	if r.text(column) == "" {
		return nil, nil
	}
	v, err := r.amount(column)
	return &v, err
}

func (r sheetRow) date(column string) (*time.Time, error) {
	text := r.text(column)
	if text == "" {
		return nil, nil
	}
	day, err := r.wb.DateOf(text)
	if err != nil {
		return nil, r.invalid(column)
	}
	return &day, nil
}

func (r sheetRow) position() (statement.P2PPosition, error) {
	p := statement.P2PPosition{
		Line: r.line, LoanNumber: cut(r.text(columnLoan), maxLoanNumber), Name: cut(r.text(columnName), maxName),
		Repayment: cut(r.text(columnRepayment), maxRepayment), Status: cut(r.text(columnStatus), maxLabel),
		RepaymentStatus: cut(r.text(columnRepaymentStatus), maxLabel),
	}
	amounts := map[string]int64{}
	for _, column := range []string{columnPrincipal, columnMarketBought, columnMarketSold, columnMarketSoldFor,
		columnMarketBoughtFor, columnOverdue, columnPenaltyInterest, columnInterestPaid, columnPrincipalReturned,
		columnFees, columnMarketFees} {
		v, err := r.amount(column)
		if err != nil {
			return p, err
		}
		amounts[column] = v
	}
	p.InvestedMinor = amounts[columnPrincipal] + amounts[columnMarketBought]
	p.OutstandingMinor = max(0, p.InvestedMinor-amounts[columnMarketSold]-amounts[columnPrincipalReturned])
	p.MarketGainMinor = amounts[columnMarketSoldFor] - amounts[columnMarketSold] -
		(amounts[columnMarketBoughtFor] - amounts[columnMarketBought])
	p.OverdueMinor = amounts[columnOverdue]
	p.PenaltyInterestMinor = amounts[columnPenaltyInterest]
	p.InterestPaidMinor = amounts[columnInterestPaid]
	p.PrincipalReturnedMinor = amounts[columnPrincipalReturned]
	p.FeesMinor = amounts[columnFees] + amounts[columnMarketFees]

	var err error
	if text := r.text(columnRate); text != "" {
		if !plainRate.MatchString(text) {
			return p, r.invalid(columnRate)
		}
		if p.InterestRate, err = decimal.NewFromString(text); err != nil || p.InterestRate.GreaterThanOrEqual(maxRate) {
			return p, r.invalid(columnRate)
		}
	}
	if p.NextPaymentMinor, err = r.optionalAmount(columnNextPayment); err != nil {
		return p, err
	}
	if p.InvestedOn, err = r.date(columnInvestedOn); err != nil {
		return p, err
	}
	if p.NextPaymentOn, err = r.date(columnNextPaymentOn); err != nil {
		return p, err
	}
	if p.FinalPaymentOn, err = r.date(columnFinalPaymentOn); err != nil {
		return p, err
	}
	if text := r.text(columnDelay); text != "" {
		d, err := decimal.NewFromString(text)
		if err != nil || !plainDays.MatchString(text) {
			return p, r.invalid(columnDelay)
		}
		p.DelayDays = int(d.IntPart())
	}
	return p, nil
}

func cut(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}
