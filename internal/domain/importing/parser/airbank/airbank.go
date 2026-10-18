// Package airbank parses the Air Bank account history CSV. Built from community parsers,
// not a real export: coin-book/importer-formaty.md, section Air Bank.
package airbank

import (
	"bytes"
	"encoding/csv"
	"errors"
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

const Format statement.Format = "airbank-cz"

const version = 1

const bankCode = "3030"

// Column names as Air Bank writes them; the bank renames and reorders columns now and
// then, so rows are read by name and only these two identify the format.
const (
	columnExecuted       = "datum provedeni"
	columnPosted         = "datum zauctovani"
	columnAmount         = "castka v mene uctu"
	columnFee            = "poplatek v mene uctu"
	columnCurrency       = "mena uctu"
	columnType           = "typ uhrady"
	columnCounterparty   = "nazev protistrany"
	columnCounterpartyNo = "cislo uctu protistrany"
	columnNote           = "poznamka k uhrade"
	columnMessage        = "zprava pro prijemce"
	columnVS             = "variabilni symbol"
	columnKS             = "konstantni symbol"
	columnSS             = "specificky symbol"
	columnReference      = "referencni cislo"
	columnCategory       = "kategorie plateb"
)

var (
	ErrMissingColumn  = eris.New("airbank: a required column is missing")
	ErrNoTransactions = eris.New("airbank: the file has no booked transactions")
)

var (
	utf8BOM         = []byte{0xEF, 0xBB, 0xBF}
	dayMonthYear    = regexp.MustCompile(`^(\d{1,2})[./](\d{1,2})[./](\d{4})`)
	accountFileName = regexp.MustCompile(`(?i)airbank_((?:\d{1,6}-)?\d{2,10})_`)
)

var diacritics = strings.NewReplacer("á", "a", "č", "c", "ď", "d", "é", "e", "ě", "e", "í", "i", "ň", "n", "ó", "o",
	"ř", "r", "š", "s", "ť", "t", "ú", "u", "ů", "u", "ý", "y", "ž", "z")

type Parser struct{}

func (Parser) Format() statement.Format { return Format }

func (Parser) Version() int { return version }

func (Parser) Detect(head []byte) statement.Confidence {
	header, _, _ := bytes.Cut(decode(bytes.TrimPrefix(head, utf8BOM)), []byte("\n"))
	columns := headerColumns(string(header))
	_, executed := columns[columnExecuted]
	_, amount := columns[columnAmount]
	if !executed || !amount {
		return statement.NoMatch
	}
	return statement.Certain
}

// AccountNumberFromFileName reads the account Air Bank names its exports after
// ("airbank_1234567890_2026-10-18_12-00.csv"); empty when the name does not carry it.
func (Parser) AccountNumberFromFileName(name string) string {
	m := accountFileName.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	return m[1] + "/" + bankCode
}

func (Parser) Parse(r io.Reader) (*statement.Statement, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, eris.Wrap(err, "read file")
	}
	text := decode(bytes.TrimPrefix(raw, utf8BOM))
	firstLine, _, _ := bytes.Cut(text, []byte("\n"))
	reader := csv.NewReader(bytes.NewReader(text))
	reader.Comma = delimiter(string(firstLine))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		return nil, eris.Wrap(err, "read header")
	}
	columns := headerColumns(strings.Join(header, string(reader.Comma)))
	for _, required := range []string{columnExecuted, columnAmount} {
		if _, ok := columns[required]; !ok {
			return nil, eris.Wrapf(ErrMissingColumn, "%s", required)
		}
	}

	accounts := map[money.Currency]*statement.Account{}
	st := &statement.Statement{Format: Format, Version: version}
	line := 1
	for {
		fields, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			return nil, eris.Wrapf(err, "line %d", line)
		}
		get := func(name string) string {
			i, ok := columns[name]
			if !ok || i >= len(fields) {
				return ""
			}
			return strings.TrimSpace(fields[i])
		}
		if get(columnExecuted) == "" {
			continue
		}
		// A card payment waits for its posting; its amount may still change, so it comes with a later export.
		if _, hasPosted := columns[columnPosted]; hasPosted && get(columnPosted) == "" {
			continue
		}
		currency := money.Currency(strings.ToUpper(get(columnCurrency)))
		if currency == "" {
			currency = money.CZK
		}
		if !currency.Valid() {
			return nil, eris.Errorf("line %d: currency %q is not an ISO 4217 code", line, currency)
		}
		row, err := parseRow(get, currency, line)
		if err != nil {
			return nil, eris.Wrapf(err, "line %d", line)
		}
		acc, ok := accounts[currency]
		if !ok {
			acc = &statement.Account{Ref: statement.AccountRef{Institution: account.Other, Kind: account.Current,
				Currency: currency, Name: "Air Bank " + string(currency)}}
			accounts[currency] = acc
			st.Accounts = append(st.Accounts, acc)
		}
		acc.Rows = append(acc.Rows, row)
	}
	if len(st.Accounts) == 0 {
		return nil, ErrNoTransactions
	}
	setPeriod(st)
	return st, nil
}

// parseRow counts the fee into the amount: Air Bank lists it beside the amount, and the
// account's balance moves by both.
func parseRow(get func(string) string, currency money.Currency, line int) (statement.Row, error) {
	bookedOn, err := parseDate(get(columnExecuted))
	if err != nil {
		return statement.Row{}, err
	}
	amount, err := parseAmount(get(columnAmount), currency)
	if err != nil {
		return statement.Row{}, eris.Wrap(err, "amount")
	}
	var fee int64
	if raw := get(columnFee); raw != "" {
		if fee, err = parseAmount(raw, currency); err != nil {
			return statement.Row{}, eris.Wrap(err, "fee")
		}
		if fee < 0 {
			fee = -fee
		}
	}
	row := statement.Row{
		Line:                line,
		BookedOn:            bookedOn,
		AmountMinor:         amount - fee,
		FeeMinor:            fee,
		OperationType:       get(columnType),
		Counterparty:        collapse(get(columnCounterparty)),
		CounterpartyAccount: strings.ReplaceAll(get(columnCounterpartyNo), " ", ""),
		Message:             joinNonEmpty(" / ", collapse(get(columnMessage)), collapse(get(columnNote))),
		VariableSymbol:      strings.TrimLeft(get(columnVS), "0"),
		ConstantSymbol:      strings.TrimLeft(get(columnKS), "0"),
		SpecificSymbol:      strings.TrimLeft(get(columnSS), "0"),
		BankCategory:        get(columnCategory),
	}
	for _, candidate := range []string{row.Counterparty, row.Message, row.OperationType} {
		if row.Description == "" {
			row.Description = candidate
		}
	}
	if reference := get(columnReference); reference != "" {
		row.IdentityKey = "ref:" + reference
	} else {
		row.IdentityKey = strings.Join([]string{bookedOn.Format(time.DateOnly), strconv.FormatInt(row.AmountMinor, 10),
			row.Description, row.CounterpartyAccount}, "|")
	}
	return row, nil
}

func parseDate(raw string) (time.Time, error) {
	m := dayMonthYear.FindStringSubmatch(raw)
	if m == nil {
		return time.Time{}, eris.Errorf("date %q: expected DD/MM/YYYY", raw)
	}
	day, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	year, _ := strconv.Atoi(m[3])
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Day() != day || int(date.Month()) != month {
		return time.Time{}, eris.Errorf("date %q", raw)
	}
	return date, nil
}

// parseAmount takes the last of ',' and '.' as the decimal mark: Air Bank writes a comma,
// a spreadsheet resaving the file may write a dot.
func parseAmount(raw string, currency money.Currency) (int64, error) {
	format := money.CommaDecimal
	if strings.LastIndex(raw, ".") > strings.LastIndex(raw, ",") {
		format = money.DotDecimal
	}
	return money.ParseMinor(raw, format, currency.Exponent())
}

// decode reads valid UTF-8 as is and anything else as Windows-1250; the client picks
// the encoding in Air Bank's settings.
func decode(raw []byte) []byte {
	if utf8.Valid(raw) {
		return raw
	}
	decoded, err := charmap.Windows1250.NewDecoder().Bytes(raw)
	if err != nil {
		return raw
	}
	return decoded
}

// delimiter is the client's choice in Air Bank's settings, either ';' or ','.
func delimiter(headerLine string) rune {
	if strings.Count(headerLine, ";") > strings.Count(headerLine, ",") {
		return ';'
	}
	return ','
}

func headerColumns(line string) map[string]int {
	reader := csv.NewReader(strings.NewReader(line))
	reader.Comma = delimiter(line)
	reader.LazyQuotes = true
	names, err := reader.Read()
	if err != nil {
		return nil
	}
	columns := map[string]int{}
	for i, name := range names {
		folded := diacritics.Replace(strings.ToLower(strings.TrimSpace(name)))
		if _, seen := columns[folded]; !seen {
			columns[folded] = i
		}
	}
	return columns
}

func joinNonEmpty(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

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
