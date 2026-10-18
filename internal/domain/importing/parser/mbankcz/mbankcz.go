// Package mbankcz parses the mBank CZ "seznam operací" CSV export.
// Format notes: coin-book/importer-formaty.md, section mBank CZ.
package mbankcz

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/rotisserie/eris"
	"golang.org/x/text/encoding/charmap"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/money"
)

const Format statement.Format = "mbank-cz"

const version = 2

const columnHeader = "#Datum operace;#Popis operace;#Účet;#Kategorie;#Částka"

const dataFieldCount = 7

var (
	ErrNoColumnHeader = eris.New("mbankcz: column header not found")
	ErrNoAccount      = eris.New("mbankcz: account number not found in the statement header")
)

var (
	accountNumberPattern  = regexp.MustCompile(`(\d{1,6})-(\d{1,10})/(\d{4})`)
	periodPattern         = regexp.MustCompile(`^(\d{2}\.\d{2}\.\d{4});(\d{2}\.\d{2}\.\d{4})`)
	counterpartyAccount   = regexp.MustCompile(`^(\d{6}) (\d{10}) (\d{4})$`)
	paymentSymbol         = regexp.MustCompile(`\b(VS|KS|SS):(\d+)`)
	segmentSeparator      = regexp.MustCompile(`\s{2,}`)
	summaryCurrencyColumn = regexp.MustCompile(`^[A-Z]{3}$`)
)

// operationTypes are the segments mBank uses to name the kind of operation. Its
// position in the description varies (a card payment puts the merchant first), so
// it is recognised by vocabulary rather than by place.
var operationTypes = map[string]bool{
	"PLATBA KARTOU":            true,
	"PLATBA NA INTERNETU":      true,
	"ODCHOZÍ PLATBA":           true,
	"ODCHOZÍ OKAMŽITÁ PLATBA":  true,
	"PŘÍCHOZÍ PLATBA":          true,
	"PŘÍCHOZÍ OKAMŽITÁ PLATBA": true,
	"VLASTNÍ PŘEVOD":           true,
	"PŘEVOD NA MSPOŘENÍ":       true,
	"PŘEVOD Z MSPOŘENÍ":        true,
	"TRVALÝ PŘÍKAZ":            true,
	"INKASO":                   true,
	"SIPO":                     true,
	"VÝBĚR Z BANKOMATU":        true,
	"VÝBĚR HOTOVOSTI":          true,
	"VKLAD HOTOVOSTI":          true,
	"POPLATEK":                 true,
	"ÚROK":                     true,
	"SPLÁTKA ÚVĚRU":            true,
}

type Parser struct{}

func (Parser) Format() statement.Format { return Format }

func (Parser) Version() int { return version }

func (Parser) Detect(head []byte) statement.Confidence {
	decoded, err := charmap.Windows1250.NewDecoder().Bytes(head)
	if err != nil || !bytes.Contains(decoded, []byte(columnHeader)) {
		return statement.NoMatch
	}
	return statement.Certain
}

func (Parser) Parse(r io.Reader) (*statement.Statement, error) {
	lines := bufio.NewScanner(charmap.Windows1250.NewDecoder().Reader(r))
	lines.Buffer(make([]byte, 64*1024), 1024*1024)

	st := &statement.Statement{Format: Format, Version: version}
	acc := &statement.Account{}
	header, err := parseHeader(lines, st, acc)
	if err != nil {
		return nil, err
	}
	if acc.Ref.AccountNumber == nil {
		return nil, ErrNoAccount
	}

	lineNo := header
	for lines.Scan() {
		lineNo++
		fields, err := splitLine(lines.Text())
		if err != nil {
			return nil, eris.Wrapf(err, "line %d", lineNo)
		}
		if len(fields) != dataFieldCount {
			continue
		}
		row, err := parseRow(fields, lineNo)
		if err != nil {
			return nil, eris.Wrapf(err, "line %d", lineNo)
		}
		if acc.Ref.Name == "" {
			acc.Ref.Name = strings.TrimSpace(fields[2])
		}
		acc.Rows = append(acc.Rows, row)
	}
	if err := lines.Err(); err != nil {
		return nil, eris.Wrap(err, "read statement")
	}
	st.Accounts = []*statement.Account{acc}
	return st, nil
}

// parseHeader consumes the preamble up to and including the column header and
// returns that header's 1-based line number.
func parseHeader(lines *bufio.Scanner, st *statement.Statement, acc *statement.Account) (int, error) {
	acc.Ref = statement.AccountRef{Institution: account.MBankCZ, Kind: account.Current, Currency: money.CZK}
	inSummary := false
	lineNo := 0
	for lines.Scan() {
		lineNo++
		line := strings.TrimSpace(lines.Text())
		switch {
		case strings.HasPrefix(line, columnHeader):
			return lineNo, nil
		case strings.HasPrefix(line, "#Měna;"):
			inSummary = true
			continue
		case acc.Ref.AccountNumber == nil && accountNumberPattern.MatchString(line):
			number := NormalizeAccountNumber(accountNumberPattern.FindStringSubmatch(line)[1:]...)
			acc.Ref.AccountNumber = &number
		case periodPattern.MatchString(line):
			m := periodPattern.FindStringSubmatch(line)
			from, errFrom := time.Parse("02.01.2006", m[1])
			to, errTo := time.Parse("02.01.2006", m[2])
			if errFrom == nil && errTo == nil {
				st.PeriodFrom, st.PeriodTo = &from, &to
			}
		}
		if inSummary {
			if err := parseSummary(line, acc); err != nil {
				return 0, err
			}
			inSummary = summaryCurrencyColumn.MatchString(strings.Split(line, ";")[0])
		}
	}
	if err := lines.Err(); err != nil {
		return 0, eris.Wrap(err, "read statement header")
	}
	return 0, ErrNoColumnHeader
}

func parseSummary(line string, acc *statement.Account) error {
	fields := strings.Split(line, ";")
	if len(fields) < 3 || money.Currency(fields[0]) != acc.Ref.Currency {
		return nil
	}
	income, err := money.ParseMinor(fields[1], money.CommaDecimal, acc.Ref.Currency.Exponent())
	if err != nil {
		return eris.Wrap(err, "summary income")
	}
	expense, err := money.ParseMinor(fields[2], money.CommaDecimal, acc.Ref.Currency.Exponent())
	if err != nil {
		return eris.Wrap(err, "summary expense")
	}
	acc.Totals = &statement.Totals{IncomeMinor: income, ExpenseMinor: expense}
	return nil
}

func splitLine(line string) ([]string, error) {
	reader := csv.NewReader(strings.NewReader(line))
	reader.Comma = ';'
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	fields, err := reader.Read()
	if err == io.EOF {
		return nil, nil
	}
	return fields, err
}

func parseRow(fields []string, line int) (statement.Row, error) {
	bookedOn, err := time.Parse(time.DateOnly, strings.TrimSpace(fields[0]))
	if err != nil {
		return statement.Row{}, eris.Wrap(err, "date")
	}
	amountField := strings.TrimSpace(fields[4])
	if !strings.HasSuffix(amountField, " "+string(money.CZK)) {
		return statement.Row{}, eris.Errorf("unexpected currency in %q", amountField)
	}
	amount, err := money.ParseMinor(amountField, money.CommaDecimal, money.CZK.Exponent())
	if err != nil {
		return statement.Row{}, err
	}
	description := collapseSpaces(fields[1])
	row := statement.Row{
		Line:         line,
		BookedOn:     bookedOn,
		AmountMinor:  amount,
		Description:  description,
		BankCategory: strings.TrimSpace(fields[3]),
		IdentityKey:  bookedOn.Format(time.DateOnly) + "|" + amountField + "|" + description,
	}
	splitDescription(fields[1], &row)
	return row, nil
}

func splitDescription(raw string, row *statement.Row) {
	var rest []string
	for _, segment := range segmentSeparator.Split(strings.TrimSpace(raw), -1) {
		switch {
		case segment == "":
		case counterpartyAccount.MatchString(segment):
			m := counterpartyAccount.FindStringSubmatch(segment)
			row.CounterpartyAccount = NormalizeAccountNumber(m[1], m[2], m[3])
		case row.OperationType == "" && operationTypes[segment]:
			row.OperationType = segment
		default:
			if text := takePaymentSymbols(segment, row); text != "" {
				rest = append(rest, text)
			}
		}
	}
	if len(rest) > 0 {
		row.Counterparty = rest[0]
		rest = rest[1:]
	}
	row.Message = strings.Join(rest, " / ")
}

// takePaymentSymbols moves VS/KS/SS into the row and returns what is left of the
// segment: mBank glues symbols to the message ("NAJEMNE KS:0308 VS:2026001").
func takePaymentSymbols(segment string, row *statement.Row) string {
	for _, m := range paymentSymbol.FindAllStringSubmatch(segment, -1) {
		value := strings.TrimLeft(m[2], "0")
		switch m[1] {
		case "VS":
			row.VariableSymbol = value
		case "KS":
			row.ConstantSymbol = value
		case "SS":
			row.SpecificSymbol = value
		}
	}
	return strings.Trim(collapseSpaces(paymentSymbol.ReplaceAllString(segment, "")), " ,")
}

// NormalizeAccountNumber renders a Czech account number as "prefix-number/bank"
// without padding zeros, dropping a zero prefix.
func NormalizeAccountNumber(parts ...string) string {
	prefix := strings.TrimLeft(parts[0], "0")
	number := strings.TrimLeft(parts[1], "0")
	if prefix == "" {
		return number + "/" + parts[2]
	}
	return prefix + "-" + number + "/" + parts[2]
}

func collapseSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }
