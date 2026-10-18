// Package revolut parses the Slovak-language Revolut consolidated statement CSV.
// Format notes: coin-book/importer-formaty.md, section Revolut.
package revolut

import (
	"bytes"
	"encoding/csv"
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

const Format statement.Format = "revolut-consolidated-sk"

// version 2 reads the investment sections (ADR 209); transaction ids are unchanged.
const version = 2

const sectionEnd = "---------"

const (
	transactionSectionSuffix = "Výpisy o transakciách"
	summarySectionSuffix     = "Súhrny"
	currentAccountsSection   = "Bežné účty " + transactionSectionSuffix
	fundsSection             = "Flexibilné peňažné fondy " + transactionSectionSuffix
)

var (
	currentAccountTitle = regexp.MustCompile(`^Osobný účet \(([A-Z]{3})\)$`)
	fundTitle           = regexp.MustCompile(`^Flexibilné peňažné fondy\s+\(([A-Z]{3})\)$`)
	datePattern         = regexp.MustCompile(`^(\d{1,2})\.[\s\x{00a0}]*(\d{1,2})\.[\s\x{00a0}]*(\d{4})$`)
)

var operationTypes = map[string]string{
	"Predajca":   "merchant",
	"Dobiť účet": "topup",
	"Zameniť":    "exchange",
	"Iné":        "other",
}

// FundYieldCategory marks a flexible-fund interest row; categorisation maps it to income.
const FundYieldCategory = "Výnos fondu"

type columnSet struct {
	amount  string
	balance string
	fee     string
	// fixedBankCategory stands in for a section that has no category column.
	fixedBankCategory string
}

var (
	currentAccountColumns = columnSet{amount: "Pohyb peňazí", balance: "Zostatok", fee: "Poplatky"}
	fundColumns           = columnSet{amount: "Čistý výnos", fee: "Poplatky za službu", fixedBankCategory: FundYieldCategory}
)

type Parser struct{}

func (Parser) Format() statement.Format { return Format }

func (Parser) Version() int { return version }

func (Parser) Detect(head []byte) statement.Confidence {
	if bytes.Contains(head, []byte("Bežné účty "+summarySectionSuffix)) || bytes.Contains(head, []byte(currentAccountsSection)) {
		return statement.Certain
	}
	return statement.NoMatch
}

type parseState struct {
	topSection  string
	current     *statement.Account
	columns     columnSet
	header      map[string]int
	investments investmentState
}

func (Parser) Parse(r io.Reader) (*statement.Statement, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	st := &statement.Statement{Format: Format, Version: version}
	state := &parseState{}
	line := 0
	for {
		fields, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, eris.Wrapf(err, "line %d", line+1)
		}
		line++
		if err := state.consume(st, fields, line); err != nil {
			return nil, eris.Wrapf(err, "line %d", line)
		}
	}
	return st, nil
}

func (s *parseState) consume(st *statement.Statement, fields []string, line int) error {
	first := strings.TrimSpace(fields[0])
	if title, ok := soleValue(fields); ok && !datePattern.MatchString(title) {
		s.enterTitle(st, title)
		return nil
	}
	if s.topSection == investmentsSection {
		return s.consumeInvestment(fields, line)
	}
	if s.current == nil || first == "" {
		return nil
	}
	if s.header == nil {
		if first == "Dátum" {
			s.header = headerIndex(fields)
		}
		return nil
	}
	if !datePattern.MatchString(first) {
		return s.readTotal(fields)
	}
	row, err := s.parseRow(fields, line)
	if err != nil {
		return err
	}
	s.current.Rows = append(s.current.Rows, row)
	return nil
}

func (s *parseState) enterTitle(st *statement.Statement, title string) {
	switch {
	case title == sectionEnd:
		s.current, s.header = nil, nil
		s.investments.block = noBlock
	case strings.HasSuffix(title, transactionSectionSuffix) || strings.HasSuffix(title, summarySectionSuffix):
		s.topSection, s.current, s.header = title, nil, nil
		s.investments = investmentState{}
	case s.topSection == investmentsSection:
		s.enterInvestmentTitle(st, title)
	case s.topSection == currentAccountsSection && currentAccountTitle.MatchString(title):
		s.open(st, currentAccountTitle.FindStringSubmatch(title)[1], account.Current, "personal", "Revolut ", currentAccountColumns)
	case s.topSection == fundsSection && fundTitle.MatchString(title):
		s.open(st, fundTitle.FindStringSubmatch(title)[1], account.Fund, "fund", "Revolut fond ", fundColumns)
	}
}

func (s *parseState) open(st *statement.Statement, currency string, kind account.Kind, refKind, namePrefix string, columns columnSet) {
	ref := "revolut:" + refKind + ":" + currency
	s.current = &statement.Account{Ref: statement.AccountRef{
		Institution: account.Revolut,
		Kind:        kind,
		Currency:    money.Currency(currency),
		ExternalRef: &ref,
		Name:        namePrefix + currency,
	}}
	s.columns = columns
	s.header = nil
	st.Accounts = append(st.Accounts, s.current)
}

func (s *parseState) parseRow(fields []string, line int) (statement.Row, error) {
	currency := s.current.Ref.Currency
	bookedOn, err := parseDate(field(fields, s.header, "Dátum"))
	if err != nil {
		return statement.Row{}, err
	}
	amountRaw := field(fields, s.header, s.columns.amount)
	amount, err := money.ParseMinor(amountRaw, money.CommaDecimal, currency.Exponent())
	if err != nil {
		return statement.Row{}, eris.Wrap(err, "amount")
	}
	fee, err := optionalMinor(field(fields, s.header, s.columns.fee), currency)
	if err != nil {
		return statement.Row{}, eris.Wrap(err, "fee")
	}
	description := strings.TrimSpace(field(fields, s.header, "Popis"))
	bankCategory := strings.TrimSpace(field(fields, s.header, "Kategória"))
	if s.columns.fixedBankCategory != "" {
		bankCategory = s.columns.fixedBankCategory
	}
	row := statement.Row{
		Line:          line,
		BookedOn:      bookedOn,
		AmountMinor:   amount,
		FeeMinor:      fee,
		Description:   description,
		Counterparty:  description,
		BankCategory:  bankCategory,
		OperationType: operationTypes[bankCategory],
	}
	identity := []string{bookedOn.Format(time.DateOnly), itoa(amount), description}
	if s.columns.balance != "" {
		balance, err := money.ParseMinor(field(fields, s.header, s.columns.balance), money.CommaDecimal, currency.Exponent())
		if err != nil {
			return statement.Row{}, eris.Wrap(err, "balance")
		}
		row.BalanceAfterMinor = &balance
		identity = append(identity, itoa(balance))
	}
	row.IdentityKey = strings.Join(identity, "|")
	return row, nil
}

func (s *parseState) readTotal(fields []string) error {
	raw := field(fields, s.header, s.columns.amount)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	sum, err := money.ParseMinor(raw, money.CommaDecimal, s.current.Ref.Currency.Exponent())
	if err != nil {
		return eris.Wrap(err, "total")
	}
	s.current.DeclaredSum = &sum
	return nil
}

// headerIndex maps each column name to its first occurrence: in a foreign-currency
// account every money column repeats, first in the account's currency, then in CZK.
func headerIndex(fields []string) map[string]int {
	index := map[string]int{}
	for i, name := range fields {
		name = strings.TrimSpace(name)
		if _, seen := index[name]; !seen && name != "" {
			index[name] = i
		}
	}
	return index
}

func field(fields []string, header map[string]int, name string) string {
	i, ok := header[name]
	if !ok || i >= len(fields) {
		return ""
	}
	return fields[i]
}

func soleValue(fields []string) (string, bool) {
	value := ""
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if value != "" {
			return "", false
		}
		value = f
	}
	return value, value != ""
}

func parseDate(raw string) (time.Time, error) {
	m := datePattern.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return time.Time{}, eris.Errorf("date %q", raw)
	}
	day, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	year, _ := strconv.Atoi(m[3])
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), nil
}

func optionalMinor(raw string, currency money.Currency) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	return money.ParseMinor(raw, money.CommaDecimal, currency.Exponent())
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
