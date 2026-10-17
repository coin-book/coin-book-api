// Package generic parses the coin-book CSV format, the standard any bank or
// spreadsheet can be converted to. Columns: coin-book/importer-formaty.md, section coin-book CSV.
package generic

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/money"
)

const Format statement.Format = "coinbook-csv"

const version = 1

const accountRefPrefix = "csv:"

// Excel prepends a byte-order mark when it saves CSV as UTF-8.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

var (
	ErrMissingColumn  = eris.New("generic: a required column is missing")
	ErrMixedCurrency  = eris.New("generic: one account holds several currencies; add an account column")
	ErrNoTransactions = eris.New("generic: the file has no transactions")
)

// columnAliases maps every accepted header (lowercase, without diacritics) to its column.
var columnAliases = map[string]string{
	"date": "date", "datum": "date", "booked_on": "date",
	"amount": "amount", "castka": "amount", "suma": "amount",
	"currency": "currency", "mena": "currency",
	"description": "description", "popis": "description",
	"counterparty": "counterparty", "protistrana": "counterparty",
	"counterparty_account": "counterparty_account", "protiucet": "counterparty_account",
	"message": "message", "zprava": "message", "sprava": "message",
	"vs": "vs", "variable_symbol": "vs",
	"ks": "ks", "constant_symbol": "ks",
	"ss": "ss", "specific_symbol": "ss",
	"balance_after": "balance_after", "zustatek": "balance_after", "zostatok": "balance_after",
	"category": "category", "kategorie": "category", "kategoria": "category",
	"id":      "id",
	"account": "account", "ucet": "account",
}

var requiredColumns = []string{"date", "amount", "currency", "description"}

var (
	isoDate   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	czechDate = regexp.MustCompile(`^(\d{1,2})\.\s*(\d{1,2})\.\s*(\d{4})$`)
)

type Parser struct{}

func (Parser) Format() statement.Format { return Format }

func (Parser) Version() int { return version }

// Detect recognises the format by its header alone, so any delimiter or column order works.
func (Parser) Detect(head []byte) statement.Confidence {
	line, _, _ := bytes.Cut(bytes.TrimPrefix(head, utf8BOM), []byte("\n"))
	columns, err := headerColumns(string(line))
	if err != nil {
		return statement.NoMatch
	}
	for _, c := range requiredColumns {
		if _, ok := columns[c]; !ok {
			return statement.NoMatch
		}
	}
	return statement.Likely
}

func (Parser) Parse(r io.Reader) (*statement.Statement, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, eris.Wrap(err, "read file")
	}
	raw = bytes.TrimPrefix(raw, utf8BOM)
	firstLine, _, _ := bytes.Cut(raw, []byte("\n"))
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.Comma = delimiter(string(firstLine))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		return nil, eris.Wrap(err, "read header")
	}
	columns, err := headerColumns(strings.Join(header, string(reader.Comma)))
	if err != nil {
		return nil, err
	}
	for _, c := range requiredColumns {
		if _, ok := columns[c]; !ok {
			return nil, eris.Wrapf(ErrMissingColumn, "%s", c)
		}
	}

	st := &statement.Statement{Format: Format, Version: version}
	accounts := map[string]*statement.Account{}
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
		if blank(fields) {
			continue
		}
		get := func(name string) string {
			i, ok := columns[name]
			if !ok || i >= len(fields) {
				return ""
			}
			return strings.TrimSpace(fields[i])
		}
		acc, err := accountFor(st, accounts, get("account"), money.Currency(strings.ToUpper(get("currency"))))
		if err != nil {
			return nil, eris.Wrapf(err, "line %d", line)
		}
		row, err := parseRow(get, acc.Ref.Currency, line)
		if err != nil {
			return nil, eris.Wrapf(err, "line %d", line)
		}
		acc.Rows = append(acc.Rows, row)
	}
	if len(st.Accounts) == 0 {
		return nil, ErrNoTransactions
	}
	setPeriod(st)
	return st, nil
}

// accountFor groups rows by the account column; without one the file is a single
// account the user maps on import.
func accountFor(st *statement.Statement, byName map[string]*statement.Account, name string, currency money.Currency) (*statement.Account, error) {
	if !currency.Valid() {
		return nil, eris.Errorf("currency %q is not an ISO 4217 code", currency)
	}
	if acc, ok := byName[name]; ok {
		if acc.Ref.Currency != currency {
			return nil, eris.Wrapf(ErrMixedCurrency, "%s and %s", acc.Ref.Currency, currency)
		}
		return acc, nil
	}
	ref := statement.AccountRef{Institution: account.Other, Kind: account.Current, Currency: currency, Name: name}
	if name != "" {
		external := accountRefPrefix + name
		ref.ExternalRef = &external
	}
	acc := &statement.Account{Ref: ref}
	byName[name] = acc
	st.Accounts = append(st.Accounts, acc)
	return acc, nil
}

func parseRow(get func(string) string, currency money.Currency, line int) (statement.Row, error) {
	bookedOn, err := parseDate(get("date"))
	if err != nil {
		return statement.Row{}, err
	}
	amount, err := parseAmount(get("amount"), currency)
	if err != nil {
		return statement.Row{}, eris.Wrap(err, "amount")
	}
	row := statement.Row{
		Line:                line,
		BookedOn:            bookedOn,
		AmountMinor:         amount,
		Description:         get("description"),
		Counterparty:        get("counterparty"),
		CounterpartyAccount: get("counterparty_account"),
		Message:             get("message"),
		VariableSymbol:      get("vs"),
		ConstantSymbol:      get("ks"),
		SpecificSymbol:      get("ss"),
		CategoryHint:        get("category"),
	}
	if row.Description == "" {
		row.Description = row.Counterparty
	}
	if raw := get("balance_after"); raw != "" {
		balance, err := parseAmount(raw, currency)
		if err != nil {
			return statement.Row{}, eris.Wrap(err, "balance_after")
		}
		row.BalanceAfterMinor = &balance
	}
	if id := get("id"); id != "" {
		row.IdentityKey = "id:" + id
	} else {
		row.IdentityKey = strings.Join([]string{bookedOn.Format(time.DateOnly), strconv.FormatInt(amount, 10),
			row.Description, row.Counterparty}, "|")
	}
	return row, nil
}

// parseAmount accepts "1 234,56", "1,234.56", "-89" and "1234.5": the last of ',' and '.' is the decimal mark.
func parseAmount(raw string, currency money.Currency) (int64, error) {
	format := money.CommaDecimal
	if strings.LastIndex(raw, ".") > strings.LastIndex(raw, ",") {
		format = money.DotDecimal
	}
	return money.ParseMinor(raw, format, currency.Exponent())
}

func parseDate(raw string) (time.Time, error) {
	if isoDate.MatchString(raw) {
		return time.Parse(time.DateOnly, raw)
	}
	if m := czechDate.FindStringSubmatch(raw); m != nil {
		day, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		year, _ := strconv.Atoi(m[3])
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), nil
	}
	return time.Time{}, eris.Errorf("date %q: expected YYYY-MM-DD or D. M. YYYY", raw)
}

func headerColumns(line string) (map[string]int, error) {
	reader := csv.NewReader(strings.NewReader(line))
	reader.Comma = delimiter(line)
	names, err := reader.Read()
	if err != nil {
		return nil, eris.Wrap(err, "read header")
	}
	columns := map[string]int{}
	for i, name := range names {
		if column, ok := columnAliases[fold(name)]; ok {
			if _, seen := columns[column]; !seen {
				columns[column] = i
			}
		}
	}
	return columns, nil
}

func delimiter(headerLine string) rune {
	if strings.Count(headerLine, ";") > strings.Count(headerLine, ",") {
		return ';'
	}
	return ','
}

var diacritics = strings.NewReplacer("á", "a", "č", "c", "ď", "d", "é", "e", "ě", "e", "í", "i", "ň", "n", "ó", "o",
	"ř", "r", "š", "s", "ť", "t", "ú", "u", "ů", "u", "ý", "y", "ž", "z", "ľ", "l", "ĺ", "l", "ô", "o", "ä", "a", "ŕ", "r")

func fold(name string) string {
	return diacritics.Replace(strings.ToLower(strings.TrimSpace(name)))
}

func blank(fields []string) bool {
	for _, f := range fields {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

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
