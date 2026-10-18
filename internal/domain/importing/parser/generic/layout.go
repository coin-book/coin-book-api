package generic

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rotisserie/eris"
	"golang.org/x/text/encoding/charmap"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/money"
)

// MappedFormat is a CSV read by a column layout the user drew in the mapper.
const MappedFormat statement.Format = "mapped-csv"

const mappedVersion = 1

const mappedRefPrefix = "mapped:"

// IsMappedRef tells an account reference the mapper made up from the column names,
// which names no real account and means nothing to show.
func IsMappedRef(ref string) bool { return strings.HasPrefix(ref, mappedRefPrefix) }

type Field string

const (
	FieldDate                Field = "date"
	FieldAmount              Field = "amount"
	FieldCredit              Field = "credit"
	FieldDebit               Field = "debit"
	FieldCurrency            Field = "currency"
	FieldDescription         Field = "description"
	FieldCounterparty        Field = "counterparty"
	FieldCounterpartyAccount Field = "counterparty_account"
	FieldBankCode            Field = "bank_code"
	FieldMessage             Field = "message"
	FieldVariableSymbol      Field = "vs"
	FieldConstantSymbol      Field = "ks"
	FieldSpecificSymbol      Field = "ss"
	FieldBalanceAfter        Field = "balance_after"
	FieldID                  Field = "id"
)

var Fields = []Field{FieldDate, FieldAmount, FieldCredit, FieldDebit, FieldCurrency, FieldDescription, FieldCounterparty,
	FieldCounterpartyAccount, FieldBankCode, FieldMessage, FieldVariableSymbol, FieldConstantSymbol, FieldSpecificSymbol,
	FieldBalanceAfter, FieldID}

type Encoding string

const (
	UTF8        Encoding = "utf-8"
	Windows1250 Encoding = "windows-1250"
)

type DateOrder string

const (
	DayMonthYear DateOrder = "dmy"
	MonthDayYear DateOrder = "mdy"
	YearMonthDay DateOrder = "ymd"
)

// Layout says how to read a bank's CSV: which row names the columns and which column
// holds which field. Column indexes are 0-based.
type Layout struct {
	Encoding  Encoding       `json:"encoding"`
	Delimiter string         `json:"delimiter"`
	HeaderRow int            `json:"header_row"`
	Columns   map[Field]int  `json:"columns"`
	DateOrder DateOrder      `json:"date_order"`
	Currency  money.Currency `json:"currency"`
}

var (
	ErrInvalidLayout  = eris.New("generic: invalid column layout")
	ErrLayoutMismatch = eris.New("generic: the file does not fit the column layout")
)

const inspectRows = 40

var delimiterCandidates = []rune{';', ',', '\t', '|'}

var dateParts = regexp.MustCompile(`^(\d{1,4})[./\-]\s*(\d{1,2})[./\-]\s*(\d{1,4})(?:[ T].*)?$`)

// Validate refuses a layout the parser could not apply; fields the file names must
// point at distinct columns.
func (l Layout) Validate() error {
	if utf8.RuneCountInString(l.Delimiter) != 1 {
		return eris.Wrap(ErrInvalidLayout, "delimiter must be one character")
	}
	if l.Encoding != UTF8 && l.Encoding != Windows1250 {
		return eris.Wrapf(ErrInvalidLayout, "encoding %q", l.Encoding)
	}
	if l.HeaderRow < 0 {
		return eris.Wrap(ErrInvalidLayout, "the file needs a row with column names")
	}
	switch l.DateOrder {
	case DayMonthYear, MonthDayYear, YearMonthDay:
	default:
		return eris.Wrapf(ErrInvalidLayout, "date order %q", l.DateOrder)
	}
	seen := map[int]Field{}
	for field, column := range l.Columns {
		if !knownField(field) {
			return eris.Wrapf(ErrInvalidLayout, "unknown field %q", field)
		}
		if column < 0 {
			return eris.Wrapf(ErrInvalidLayout, "%s: negative column", field)
		}
		if other, taken := seen[column]; taken {
			return eris.Wrapf(ErrInvalidLayout, "%s and %s share column %d", field, other, column)
		}
		seen[column] = field
	}
	if !l.has(FieldDate) {
		return eris.Wrap(ErrInvalidLayout, "a date column is required")
	}
	if l.has(FieldAmount) == (l.has(FieldCredit) || l.has(FieldDebit)) {
		return eris.Wrap(ErrInvalidLayout, "either an amount column, or credit and debit columns")
	}
	if !l.has(FieldDescription) && !l.has(FieldCounterparty) && !l.has(FieldMessage) {
		return eris.Wrap(ErrInvalidLayout, "a description, counterparty or message column is required")
	}
	if !l.has(FieldCurrency) && !l.Currency.Valid() {
		return eris.Wrap(ErrInvalidLayout, "a currency column or a fixed currency is required")
	}
	return nil
}

func (l Layout) has(f Field) bool {
	_, ok := l.Columns[f]
	return ok
}

func knownField(f Field) bool {
	for _, known := range Fields {
		if f == known {
			return true
		}
	}
	return false
}

// Fingerprint identifies the bank's export shape by its column names, so a saved
// layout recognises the next file of the same shape. Empty when the header row is missing.
func (l Layout) Fingerprint(raw []byte) string {
	records, err := readRecords(raw, l.Encoding, []rune(l.Delimiter)[0], l.HeaderRow+1)
	if err != nil || len(records) <= l.HeaderRow {
		return ""
	}
	names := make([]string, len(records[l.HeaderRow]))
	for i, name := range records[l.HeaderRow] {
		names[i] = fold(name)
	}
	if strings.Join(names, "") == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(string(l.Encoding) + "\x00" + l.Delimiter + "\x00" + strings.Join(names, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// ParseLayout reads rows between the header and the end. Rows before the first and
// after the last row with a date (bank summaries, footers) are skipped; a row in
// between that does not fit is an error, not something to drop silently.
func ParseLayout(raw []byte, layout Layout) (*statement.Statement, error) {
	if err := layout.Validate(); err != nil {
		return nil, err
	}
	fingerprint := layout.Fingerprint(raw)
	if fingerprint == "" {
		return nil, eris.Wrap(ErrLayoutMismatch, "no column names on the header row")
	}
	records, err := readRecords(raw, layout.Encoding, []rune(layout.Delimiter)[0], -1)
	if err != nil {
		return nil, eris.Wrap(ErrLayoutMismatch, err.Error())
	}
	data := records[layout.HeaderRow+1:]
	first, last := -1, -1
	for i, fields := range data {
		if _, err := parseOrderedDate(cell(fields, layout, FieldDate), layout.DateOrder); err == nil {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return nil, eris.Wrap(ErrLayoutMismatch, "no row has a date in the date column")
	}

	st := &statement.Statement{Format: MappedFormat, Version: mappedVersion}
	accounts := map[money.Currency]*statement.Account{}
	for i := first; i <= last; i++ {
		fields := data[i]
		line := layout.HeaderRow + 2 + i
		if blank(fields) {
			continue
		}
		row, currency, err := mappedRow(fields, layout, line)
		if err != nil {
			return nil, eris.Wrapf(ErrLayoutMismatch, "line %d: %v", line, err)
		}
		acc, ok := accounts[currency]
		if !ok {
			ref := mappedRefPrefix + fingerprint + ":" + string(currency)
			acc = &statement.Account{Ref: statement.AccountRef{Institution: account.Other, Kind: account.Current,
				Currency: currency, ExternalRef: &ref}}
			accounts[currency] = acc
			st.Accounts = append(st.Accounts, acc)
		}
		acc.Rows = append(acc.Rows, row)
	}
	setPeriod(st)
	return st, nil
}

func mappedRow(fields []string, layout Layout, line int) (statement.Row, money.Currency, error) {
	get := func(f Field) string { return cell(fields, layout, f) }
	currency := layout.Currency
	if layout.has(FieldCurrency) {
		currency = money.Currency(strings.ToUpper(get(FieldCurrency)))
	}
	if !currency.Valid() {
		return statement.Row{}, "", eris.Errorf("currency %q is not an ISO 4217 code", currency)
	}
	bookedOn, err := parseOrderedDate(get(FieldDate), layout.DateOrder)
	if err != nil {
		return statement.Row{}, "", err
	}
	amount, err := mappedAmount(get, currency)
	if err != nil {
		return statement.Row{}, "", err
	}
	row := statement.Row{
		Line:                line,
		BookedOn:            bookedOn,
		AmountMinor:         amount,
		Description:         collapse(get(FieldDescription)),
		Counterparty:        collapse(get(FieldCounterparty)),
		CounterpartyAccount: counterpartyAccount(get(FieldCounterpartyAccount), get(FieldBankCode)),
		Message:             collapse(get(FieldMessage)),
		VariableSymbol:      strings.TrimLeft(get(FieldVariableSymbol), "0"),
		ConstantSymbol:      strings.TrimLeft(get(FieldConstantSymbol), "0"),
		SpecificSymbol:      strings.TrimLeft(get(FieldSpecificSymbol), "0"),
	}
	for _, fallback := range []string{row.Counterparty, row.Message, row.CounterpartyAccount} {
		if row.Description != "" {
			break
		}
		row.Description = fallback
	}
	if raw := get(FieldBalanceAfter); raw != "" {
		balance, err := parseAmount(raw, currency)
		if err != nil {
			return statement.Row{}, "", eris.Wrap(err, "balance")
		}
		row.BalanceAfterMinor = &balance
	}
	if id := get(FieldID); id != "" {
		row.IdentityKey = "id:" + id
	} else {
		row.IdentityKey = strings.Join([]string{bookedOn.Format(time.DateOnly), strconv.FormatInt(amount, 10),
			row.Description, row.Counterparty, row.Message}, "|")
	}
	return row, currency, nil
}

// mappedAmount takes the signed amount, or credit minus debit: banks with two columns
// write the debit positive or negative, so only its size counts.
func mappedAmount(get func(Field) string, currency money.Currency) (int64, error) {
	if raw := get(FieldAmount); raw != "" {
		return parseAmount(raw, currency)
	}
	credit, debit := get(FieldCredit), get(FieldDebit)
	if credit == "" && debit == "" {
		return 0, eris.New("no amount")
	}
	var total int64
	if credit != "" {
		value, err := parseAmount(credit, currency)
		if err != nil {
			return 0, eris.Wrap(err, "credit")
		}
		total += abs(value)
	}
	if debit != "" {
		value, err := parseAmount(debit, currency)
		if err != nil {
			return 0, eris.Wrap(err, "debit")
		}
		total -= abs(value)
	}
	return total, nil
}

func counterpartyAccount(number, bankCode string) string {
	number, bankCode = strings.TrimSpace(number), strings.TrimSpace(bankCode)
	if number == "" || bankCode == "" || strings.Contains(number, "/") {
		return number
	}
	return number + "/" + bankCode
}

// parseOrderedDate reads three numbers in the layout's order; a four-digit first number
// is always the year, a two-digit year is this century. A time after the date is ignored.
func parseOrderedDate(raw string, order DateOrder) (time.Time, error) {
	m := dateParts.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return time.Time{}, eris.Errorf("date %q", raw)
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	c, _ := strconv.Atoi(m[3])
	var year, month, day int
	switch {
	case len(m[1]) == 4, order == YearMonthDay:
		year, month, day = a, b, c
	case order == MonthDayYear:
		month, day, year = a, b, c
	default:
		day, month, year = a, b, c
	}
	if year < 100 {
		year += 2000
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Day() != day || int(date.Month()) != month {
		return time.Time{}, eris.Errorf("date %q", raw)
	}
	return date, nil
}

func cell(fields []string, layout Layout, f Field) string {
	i, ok := layout.Columns[f]
	if !ok || i >= len(fields) {
		return ""
	}
	return strings.TrimSpace(fields[i])
}

// readRecords decodes the file and splits it; limit < 0 reads every record.
func readRecords(raw []byte, encoding Encoding, delimiter rune, limit int) ([][]string, error) {
	text, err := decodeText(raw, encoding)
	if err != nil {
		return nil, err
	}
	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	var records [][]string
	for limit < 0 || len(records) < limit {
		fields, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, eris.Wrap(err, "split rows")
		}
		records = append(records, fields)
	}
	return records, nil
}

func decodeText(raw []byte, encoding Encoding) (string, error) {
	raw = bytes.TrimPrefix(raw, utf8BOM)
	if encoding == Windows1250 {
		decoded, err := charmap.Windows1250.NewDecoder().Bytes(raw)
		return string(decoded), eris.Wrap(err, "decode Windows-1250")
	}
	if !utf8.Valid(raw) {
		return "", eris.New("the file is not UTF-8; choose Windows-1250")
	}
	return string(raw), nil
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Inspection is what the mapper shows: the first rows split into cells, and a layout
// guessed from them for the user to correct.
type Inspection struct {
	Rows      [][]string
	Suggested Layout
}

// Inspect guesses the encoding and delimiter unless given, then the header row and the
// columns. An empty encoding or delimiter means "guess".
func Inspect(raw []byte, encoding Encoding, delimiter string) (*Inspection, error) {
	if encoding == "" {
		encoding = guessEncoding(raw)
	}
	text, err := decodeText(raw, encoding)
	if err != nil {
		return nil, eris.Wrap(ErrLayoutMismatch, err.Error())
	}
	if utf8.RuneCountInString(delimiter) != 1 {
		delimiter = string(guessDelimiter(text))
	}
	records, err := readRecords(raw, encoding, []rune(delimiter)[0], inspectRows)
	if err != nil {
		return nil, eris.Wrap(ErrLayoutMismatch, err.Error())
	}
	if len(records) == 0 {
		return nil, eris.Wrap(ErrLayoutMismatch, "the file is empty")
	}
	layout := Layout{Encoding: encoding, Delimiter: delimiter, HeaderRow: guessHeaderRow(records), Columns: map[Field]int{},
		DateOrder: DayMonthYear}
	layout.Columns = guessColumns(records[layout.HeaderRow])
	data := records[layout.HeaderRow+1:]
	if !layout.has(FieldDate) {
		if column, ok := dateColumn(data, len(records[layout.HeaderRow])); ok {
			layout.Columns[FieldDate] = column
		}
	}
	if column, ok := layout.Columns[FieldDate]; ok {
		layout.DateOrder = guessDateOrder(data, column)
	}
	if !layout.has(FieldCurrency) {
		layout.Currency = guessCurrency(data, layout)
	}
	return &Inspection{Rows: records, Suggested: layout}, nil
}

func guessEncoding(raw []byte) Encoding {
	if utf8.Valid(raw) {
		return UTF8
	}
	return Windows1250
}

// guessDelimiter takes the candidate that splits the most lines into the same number
// of cells; a tie goes to more cells.
func guessDelimiter(text string) rune {
	lines := strings.Split(text, "\n")
	lines = lines[:min(len(lines), inspectRows)]
	best, bestLines, bestCells := ';', 0, 0
	for _, candidate := range delimiterCandidates {
		counts := map[int]int{}
		for _, line := range lines {
			if n := strings.Count(line, string(candidate)); n > 0 {
				counts[n]++
			}
		}
		for cells, n := range counts {
			if n > bestLines || (n == bestLines && cells > bestCells) {
				best, bestLines, bestCells = candidate, n, cells
			}
		}
	}
	return best
}

// guessHeaderRow is the first row of mostly words that a row with a date follows:
// banks put account details and a period above the column names.
func guessHeaderRow(records [][]string) int {
	for i := 0; i+1 < len(records); i++ {
		words, filled := 0, 0
		for _, cell := range records[i] {
			cell = strings.TrimSpace(cell)
			if cell == "" {
				continue
			}
			filled++
			if strings.IndexFunc(cell, unicode.IsLetter) >= 0 && !looksLikeDate(cell) {
				words++
			}
		}
		if filled >= 3 && words*2 >= filled && rowHasDate(records[i+1]) {
			return i
		}
	}
	return 0
}

func rowHasDate(fields []string) bool {
	for _, f := range fields {
		if looksLikeDate(f) {
			return true
		}
	}
	return false
}

func looksLikeDate(s string) bool { return dateParts.MatchString(strings.TrimSpace(s)) }

func guessColumns(header []string) map[Field]int {
	columns := map[Field]int{}
	for i, name := range header {
		field, ok := headerFields[strings.Trim(fold(name), "#:*. ")]
		if !ok {
			continue
		}
		if _, taken := columns[field]; !taken {
			columns[field] = i
		}
	}
	return columns
}

func dateColumn(data [][]string, width int) (int, bool) {
	for column := 0; column < width; column++ {
		dates := 0
		for _, fields := range data {
			if column < len(fields) && looksLikeDate(fields[column]) {
				dates++
			}
		}
		if dates > 0 && dates*2 >= len(data) {
			return column, true
		}
	}
	return 0, false
}

func guessDateOrder(data [][]string, column int) DateOrder {
	for _, fields := range data {
		if column >= len(fields) {
			continue
		}
		m := dateParts.FindStringSubmatch(strings.TrimSpace(fields[column]))
		if m == nil {
			continue
		}
		if len(m[1]) == 4 {
			return YearMonthDay
		}
		if second, _ := strconv.Atoi(m[2]); second > 12 {
			return MonthDayYear
		}
	}
	return DayMonthYear
}

var currencyCode = regexp.MustCompile(`\b[A-Z]{3}\b`)

// guessCurrency looks for an ISO code written next to the amounts, else CZK.
func guessCurrency(data [][]string, layout Layout) money.Currency {
	for _, f := range []Field{FieldAmount, FieldCredit, FieldDebit} {
		column, ok := layout.Columns[f]
		if !ok {
			continue
		}
		for _, fields := range data {
			if column >= len(fields) {
				continue
			}
			if code := money.Currency(currencyCode.FindString(fields[column])); code.Valid() {
				return code
			}
		}
	}
	return money.CZK
}

// headerFields maps column names Czech, Slovak and English bank exports use (lowercase,
// without diacritics) to the field they hold.
var headerFields = map[string]Field{
	"datum": FieldDate, "date": FieldDate, "datum zauctovani": FieldDate, "datum provedeni": FieldDate,
	"datum transakce": FieldDate, "datum operace": FieldDate, "datum pohybu": FieldDate, "zauctovano": FieldDate,
	"datum zuctovania": FieldDate, "datum realizacie": FieldDate, "datum transakcie": FieldDate,
	"booking date": FieldDate, "transaction date": FieldDate, "completed date": FieldDate, "booked_on": FieldDate,
	"castka": FieldAmount, "suma": FieldAmount, "amount": FieldAmount, "objem": FieldAmount, "pohyb": FieldAmount,
	"castka v mene uctu": FieldAmount, "zauctovana castka": FieldAmount, "suma transakcie": FieldAmount,
	"objem transakce": FieldAmount, "castka transakce": FieldAmount,
	"prijem": FieldCredit, "kredit": FieldCredit, "credit": FieldCredit, "pripsano": FieldCredit, "prijmy": FieldCredit,
	"vydaj": FieldDebit, "debet": FieldDebit, "debit": FieldDebit, "odepsano": FieldDebit, "vydaje": FieldDebit,
	"mena": FieldCurrency, "currency": FieldCurrency, "mena uctu": FieldCurrency,
	"popis": FieldDescription, "description": FieldDescription, "popis transakce": FieldDescription,
	"popis operace": FieldDescription, "detail": FieldDescription, "informace o transakci": FieldDescription,
	"popis transakcie": FieldDescription,
	"protistrana":      FieldCounterparty, "nazev protiuctu": FieldCounterparty, "nazev protistrany": FieldCounterparty,
	"prijemce": FieldCounterparty, "obchodnik": FieldCounterparty, "merchant": FieldCounterparty, "payee": FieldCounterparty,
	"nazov protiuctu": FieldCounterparty, "nazov protistrany": FieldCounterparty, "counterparty": FieldCounterparty,
	"protiucet": FieldCounterpartyAccount, "cislo protiuctu": FieldCounterpartyAccount,
	"ucet protistrany": FieldCounterpartyAccount, "cislo uctu protistrany": FieldCounterpartyAccount,
	"counterparty account": FieldCounterpartyAccount, "iban protistrany": FieldCounterpartyAccount,
	"cislo protiuctu a kod banky": FieldCounterpartyAccount, "counterparty_account": FieldCounterpartyAccount,
	"kod banky": FieldBankCode, "kod banky protiuctu": FieldBankCode, "banka protiuctu": FieldBankCode,
	"bank code": FieldBankCode,
	"zprava":    FieldMessage, "sprava": FieldMessage, "zprava pro prijemce": FieldMessage, "poznamka": FieldMessage,
	"sprava pre prijemcu": FieldMessage, "message": FieldMessage, "note": FieldMessage, "reference": FieldMessage,
	"vs": FieldVariableSymbol, "variabilni symbol": FieldVariableSymbol, "variabilny symbol": FieldVariableSymbol,
	"ks": FieldConstantSymbol, "konstantni symbol": FieldConstantSymbol, "konstantny symbol": FieldConstantSymbol,
	"ss": FieldSpecificSymbol, "specificky symbol": FieldSpecificSymbol,
	"zustatek": FieldBalanceAfter, "zostatok": FieldBalanceAfter, "balance": FieldBalanceAfter,
	"zustatek po transakci": FieldBalanceAfter,
	"id":                    FieldID, "id pohybu": FieldID, "id transakce": FieldID, "id operace": FieldID, "id transakcie": FieldID,
}
