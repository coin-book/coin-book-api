// Package abo parses the ABO (GPC) statement, the fixed-width export Czech banks
// share. Format notes: coin-book/importer-formaty.md, section ABO (GPC).
package abo

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rotisserie/eris"
	"golang.org/x/text/encoding/charmap"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/money"
)

const Format statement.Format = "abo-gpc"

const version = 1

const recordLength = 128

const (
	statementHeader = "074"
	transaction     = "075"
	messageFirst    = "078"
	messageSecond   = "079"
)

const (
	debit         = '1'
	credit        = '2'
	reverseDebit  = '4'
	reverseCredit = '5'
)

const reversalOperation = "STORNO"

var (
	ErrNoStatementHeader = eris.New("abo: a transaction comes before any statement header")
	ErrNoTransactions    = eris.New("abo: the file has no transactions")
)

var headerPattern = regexp.MustCompile(`^074\d{16}`)

// numericCurrencies maps the ISO 4217 numeric codes ABO carries (zero-padded to four digits).
var numericCurrencies = map[int]money.Currency{
	203: "CZK", 978: "EUR", 840: "USD", 826: "GBP", 985: "PLN", 348: "HUF", 756: "CHF",
	752: "SEK", 578: "NOK", 208: "DKK", 392: "JPY", 124: "CAD", 36: "AUD", 946: "RON", 975: "BGN",
}

type Parser struct{}

func (Parser) Format() statement.Format { return Format }

func (Parser) Version() int { return version }

func (Parser) Detect(head []byte) statement.Confidence {
	if !headerPattern.Match(bytes.TrimLeft(head, "\r\n")) {
		return statement.NoMatch
	}
	return statement.Certain
}

func (Parser) Parse(r io.Reader) (*statement.Statement, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, eris.Wrap(err, "read file")
	}
	text, err := decode(raw)
	if err != nil {
		return nil, err
	}

	st := &statement.Statement{Format: Format, Version: version}
	accounts := map[string]*statement.Account{}
	var current *statement.Account
	var last *statement.Row
	lines := bufio.NewScanner(strings.NewReader(text))
	lineNo := 0
	for lines.Scan() {
		lineNo++
		record := padRecord(lines.Text())
		switch string(record[:3]) {
		case statementHeader:
			current, err = accountFor(st, accounts, record)
			last = nil
		case transaction:
			if current == nil {
				return nil, eris.Wrapf(ErrNoStatementHeader, "line %d", lineNo)
			}
			var row statement.Row
			if row, err = parseTransaction(record, current, lineNo); err == nil {
				current.Rows = append(current.Rows, row)
				last = &current.Rows[len(current.Rows)-1]
			}
		case messageFirst, messageSecond:
			if last != nil {
				appendMessage(last, record)
			}
		}
		if err != nil {
			return nil, eris.Wrapf(err, "line %d", lineNo)
		}
	}
	if err := lines.Err(); err != nil {
		return nil, eris.Wrap(err, "read statement")
	}
	for _, acc := range st.Accounts {
		finishRows(acc)
	}
	setPeriod(st)
	if st.PeriodFrom == nil {
		return nil, ErrNoTransactions
	}
	return st, nil
}

// decode reads UTF-8 when the file is valid UTF-8, else Windows-1250, which banks
// default to. Columns are then counted in characters, not bytes.
func decode(raw []byte) (string, error) {
	if utf8.Valid(raw) {
		return string(raw), nil
	}
	decoded, err := charmap.Windows1250.NewDecoder().Bytes(raw)
	if err != nil {
		return "", eris.Wrap(err, "decode Windows-1250")
	}
	return string(decoded), nil
}

// padRecord restores the trailing spaces some banks strip, so every column can be sliced.
func padRecord(line string) []rune {
	record := []rune(strings.TrimRight(line, "\r"))
	for len(record) < recordLength {
		record = append(record, ' ')
	}
	return record
}

// accountFor reads a statement header. A file may hold several statements of one
// account (one per day) or of several accounts; the declared sum adds up across them.
func accountFor(st *statement.Statement, byNumber map[string]*statement.Account, record []rune) (*statement.Account, error) {
	number := accountNumber(field(record, 4, 19), "")
	opening, err := signedAmount(field(record, 46, 59), record[59])
	if err != nil {
		return nil, eris.Wrap(err, "opening balance")
	}
	closing, err := signedAmount(field(record, 61, 74), record[74])
	if err != nil {
		return nil, eris.Wrap(err, "closing balance")
	}
	acc, ok := byNumber[number]
	if !ok {
		acc = &statement.Account{
			Ref: statement.AccountRef{Institution: account.Other, Kind: account.Current, AccountNumber: &number,
				Name: strings.TrimSpace(field(record, 20, 39))},
			OpeningBalanceMinor: &opening,
			DeclaredSum:         new(int64),
		}
		byNumber[number] = acc
		st.Accounts = append(st.Accounts, acc)
	}
	*acc.DeclaredSum += closing - opening
	return acc, nil
}

func parseTransaction(record []rune, acc *statement.Account, line int) (statement.Row, error) {
	amount, err := strconv.ParseInt(field(record, 49, 60), 10, 64)
	if err != nil {
		return statement.Row{}, eris.Wrap(err, "amount")
	}
	row := statement.Row{Line: line}
	switch record[60] {
	case debit:
		row.AmountMinor = -amount
	case credit:
		row.AmountMinor = amount
	case reverseDebit:
		row.AmountMinor, row.OperationType = amount, reversalOperation
	case reverseCredit:
		row.AmountMinor, row.OperationType = -amount, reversalOperation
	default:
		return statement.Row{}, eris.Errorf("unknown posting code %q", record[60])
	}
	currency, err := currencyOf(field(record, 119, 122))
	if err != nil {
		return statement.Row{}, err
	}
	if acc.Ref.Currency == "" {
		acc.Ref.Currency = currency
	} else if acc.Ref.Currency != currency {
		return statement.Row{}, eris.Errorf("currency %s in an account held in %s", currency, acc.Ref.Currency)
	}
	row.BookedOn, err = bookingDate(field(record, 123, 128), field(record, 92, 97))
	if err != nil {
		return statement.Row{}, err
	}
	row.CounterpartyAccount = accountNumber(field(record, 20, 35), field(record, 74, 77))
	row.VariableSymbol = symbol(field(record, 62, 71))
	row.ConstantSymbol = symbol(field(record, 78, 81))
	row.SpecificSymbol = symbol(field(record, 82, 91))
	row.Counterparty = collapseSpaces(field(record, 98, 117))
	row.IdentityKey = strings.Join([]string{row.BookedOn.Format(time.DateOnly), strconv.FormatInt(row.AmountMinor, 10),
		strings.TrimSpace(field(record, 36, 48)), row.CounterpartyAccount, row.VariableSymbol, row.ConstantSymbol,
		row.SpecificSymbol, row.Counterparty}, "|")
	return row, nil
}

// appendMessage takes the message-for-recipient lines that follow a transaction.
func appendMessage(row *statement.Row, record []rune) {
	text := collapseSpaces(string(record[3:]))
	if text == "" {
		return
	}
	if row.Message != "" {
		row.Message += " "
	}
	row.Message += text
}

// finishRows names each row once its message is known: the counterparty, else the
// message, else the counterparty account.
func finishRows(acc *statement.Account) {
	if acc.Ref.Currency == "" {
		acc.Ref.Currency = money.CZK
	}
	for i := range acc.Rows {
		row := &acc.Rows[i]
		for _, candidate := range []string{row.Counterparty, row.Message, row.CounterpartyAccount} {
			if candidate != "" {
				row.Description = candidate
				break
			}
		}
	}
}

// field slices a record by the 1-based inclusive columns the ABO specification uses.
func field(record []rune, from, to int) string { return string(record[from-1 : to]) }

func signedAmount(digits string, sign rune) (int64, error) {
	amount, err := strconv.ParseInt(strings.TrimSpace(digits), 10, 64)
	if err != nil {
		return 0, err
	}
	if sign == '-' {
		return -amount, nil
	}
	return amount, nil
}

func currencyOf(code string) (money.Currency, error) {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" || strings.Trim(trimmed, "0") == "" {
		return money.CZK, nil
	}
	numeric, err := strconv.Atoi(trimmed)
	if err != nil {
		return "", eris.Errorf("currency code %q", code)
	}
	currency, ok := numericCurrencies[numeric]
	if !ok {
		return "", eris.Errorf("unsupported currency code %q", code)
	}
	return currency, nil
}

// bookingDate prefers the posting date and falls back to the value date; banks
// leave either one as zeros.
func bookingDate(posted, value string) (time.Time, error) {
	for _, raw := range []string{posted, value} {
		if strings.Trim(raw, "0 ") == "" {
			continue
		}
		date, err := time.Parse("020106", raw)
		if err != nil {
			return time.Time{}, eris.Wrapf(err, "date %q", raw)
		}
		return date, nil
	}
	return time.Time{}, eris.New("transaction has no date")
}

// accountNumber renders the 16 digits (6 prefix, 10 number) as "prefix-number/bank",
// dropping padding zeros. The own account carries no bank code in ABO.
func accountNumber(digits, bankCode string) string {
	digits = strings.TrimSpace(digits)
	if strings.Trim(digits, "0") == "" || len(digits) != 16 {
		return ""
	}
	prefix := strings.TrimLeft(digits[:6], "0")
	number := strings.TrimLeft(digits[6:], "0")
	if prefix != "" {
		number = prefix + "-" + number
	}
	if bankCode = strings.TrimSpace(bankCode); strings.Trim(bankCode, "0") != "" {
		number += "/" + bankCode
	}
	return number
}

func symbol(raw string) string { return strings.TrimLeft(strings.TrimSpace(raw), "0") }

func collapseSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

func setPeriod(st *statement.Statement) {
	for _, acc := range st.Accounts {
		for _, r := range acc.Rows {
			d := r.BookedOn
			if st.PeriodFrom == nil || d.Before(*st.PeriodFrom) {
				st.PeriodFrom = &d
			}
			if st.PeriodTo == nil || d.After(*st.PeriodTo) {
				st.PeriodTo = &d
			}
		}
	}
}
