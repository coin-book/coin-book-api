package revolut

import (
	"regexp"
	"strings"
	"time"

	"github.com/rotisserie/eris"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/money"
)

// Format notes: coin-book/importer-formaty.md, section "Investice a krypto"; model: ADR 209.
const investmentsSection = "Investment Services " + transactionSectionSuffix

const roboAdvisorTitle = "Robo-Advisor"

type investmentBlock int

const (
	noBlock investmentBlock = iota
	dividendBlock
	lotSaleBlock
	buyBlock
)

// Every money column appears twice: first in the trade's currency, then in CZK.
const (
	tradeCurrency = 0
	inCZK         = 1
)

var (
	currencyCode = regexp.MustCompile(`\b[A-Z]{3}\b`)
	// Plain digits within numeric(28,10): decimal.NewFromString also takes "1e-2147483000",
	// which costs gigabytes to print.
	quantityPattern = regexp.MustCompile(`^[0-9]{1,18}(\.[0-9]{1,10})?$`)
)

type investmentState struct {
	account *statement.Account
	block   investmentBlock
	// format differs per block: dividends use the Slovak one, trades the English one.
	format  money.NumberFormat
	columns map[string][]int
}

func (s *parseState) enterInvestmentTitle(st *statement.Statement, title string) {
	inv := &s.investments
	switch {
	case title == roboAdvisorTitle:
		inv.account = openInvestmentAccount(st, "revolut:robo", "Revolut Robo-Advisor", money.EUR)
		inv.block = noBlock
		return
	case strings.HasPrefix(title, "Výpis transakcií"):
		inv.block, inv.format = dividendBlock, money.CommaDecimal
	case title == "Predané jednotky":
		inv.block, inv.format = lotSaleBlock, money.DotDecimal
	case title == "Iné transakcie na brokerskom účte":
		inv.block, inv.format = buyBlock, money.DotDecimal
	default:
		inv.block = noBlock
		return
	}
	inv.columns = nil
	if inv.account == nil {
		inv.account = openInvestmentAccount(st, "revolut:broker", "Revolut investice", money.USD)
	}
}

// openInvestmentAccount names the account's usual currency; each record carries its own.
func openInvestmentAccount(st *statement.Statement, ref, name string, currency money.Currency) *statement.Account {
	acc := &statement.Account{
		Ref: statement.AccountRef{
			Institution: account.Revolut, Kind: account.Broker, Currency: currency, ExternalRef: &ref, Name: name,
		},
		Investments: &statement.Investments{},
	}
	st.Accounts = append(st.Accounts, acc)
	return acc
}

func (s *parseState) consumeInvestment(fields []string, line int) error {
	inv := &s.investments
	if inv.block == noBlock {
		return nil
	}
	first := strings.TrimSpace(fields[0])
	if inv.columns == nil {
		if strings.HasPrefix(first, "Dátum") {
			inv.columns = columnPositions(fields)
		}
		return nil
	}
	if !datePattern.MatchString(first) {
		return nil
	}
	r := investmentRow{fields: fields, columns: inv.columns, format: inv.format}
	records := inv.account.Investments
	switch inv.block {
	case dividendBlock:
		d, err := r.dividend(line)
		if err != nil {
			return err
		}
		records.Dividends = append(records.Dividends, d)
	case lotSaleBlock:
		l, err := r.lotSale(line)
		if err != nil {
			return err
		}
		records.LotSales = append(records.LotSales, l)
	case buyBlock:
		b, err := r.buy(line)
		if err != nil {
			return err
		}
		records.Buys = append(records.Buys, b)
	case noBlock:
	}
	return nil
}

// columnPositions keeps every occurrence of a column name, in order.
func columnPositions(fields []string) map[string][]int {
	positions := map[string][]int{}
	for i, name := range fields {
		if name = strings.TrimSpace(name); name != "" {
			positions[name] = append(positions[name], i)
		}
	}
	return positions
}

type investmentRow struct {
	fields  []string
	columns map[string][]int
	format  money.NumberFormat
}

func (r investmentRow) cell(name string, occurrence int) string {
	positions := r.columns[name]
	if occurrence >= len(positions) || positions[occurrence] >= len(r.fields) {
		return ""
	}
	return strings.TrimSpace(r.fields[positions[occurrence]])
}

func (r investmentRow) date(name string) (time.Time, error) {
	return parseDate(r.cell(name, tradeCurrency))
}

// tradeMoney reads a column in the trade's currency; an empty cell is an error.
func (r investmentRow) tradeMoney(name string) (int64, money.Currency, error) {
	raw := r.cell(name, tradeCurrency)
	currency, ok := currencyOf(raw)
	if !ok {
		return 0, "", eris.Errorf("%s: no currency in %q", name, raw)
	}
	minor, err := money.ParseMinor(raw, r.format, currency.Exponent())
	return minor, currency, eris.Wrap(err, name)
}

// czk reads a column's CZK copy; nil when it is empty or, as Revolut sometimes writes,
// not in CZK at all.
func (r investmentRow) czk(name string) (*int64, error) {
	raw := r.cell(name, inCZK)
	if currency, ok := currencyOf(raw); !ok || currency != money.CZK {
		return nil, nil
	}
	minor, err := money.ParseMinor(raw, r.format, money.CZK.Exponent())
	if err != nil {
		return nil, eris.Wrap(err, name)
	}
	return &minor, nil
}

func (r investmentRow) czkSum(names ...string) (int64, error) {
	var sum int64
	for _, name := range names {
		v, err := r.czk(name)
		if err != nil {
			return 0, err
		}
		if v != nil {
			sum += abs(*v)
		}
	}
	return sum, nil
}

func (r investmentRow) quantity(name string) (decimal.Decimal, error) {
	raw := strings.NewReplacer(",", "", " ", "", " ", "").Replace(r.cell(name, tradeCurrency))
	if !quantityPattern.MatchString(raw) {
		return decimal.Decimal{}, eris.Errorf("%s: quantity %q", name, raw)
	}
	q, err := decimal.NewFromString(raw)
	if err != nil || !q.IsPositive() {
		return decimal.Decimal{}, eris.Errorf("%s: quantity %q", name, raw)
	}
	return q, nil
}

func (r investmentRow) isin(name string, occurrence int) (string, error) {
	isin := r.cell(name, occurrence)
	if !statement.ValidISIN(isin) {
		return "", eris.Errorf("ISIN %q", isin)
	}
	return isin, nil
}

// security keeps what a statement says about an instrument only when it looks like a ticker
// and a name (statement.NewSecurity).
func security(isin, ticker, name string) statement.Security {
	return statement.NewSecurity(isin, ticker, name, "")
}

func (r investmentRow) dividend(line int) (statement.Dividend, error) {
	d := statement.Dividend{Line: line}
	var err error
	if d.PaidOn, err = r.date("Dátum"); err != nil {
		return d, err
	}
	isin, err := r.isin("ISIN", 0)
	if err != nil {
		return d, err
	}
	d.Security = security(isin, "", strings.TrimSuffix(r.cell("Popis a symbol", 0), " dividend"))
	if d.GrossMinor, d.Currency, err = r.tradeMoney("Hrubá dividenda / príjem"); err != nil {
		return d, err
	}
	if d.NetMinor, _, err = r.tradeMoney("Čistá dividenda / príjem"); err != nil {
		return d, err
	}
	if d.GrossCZKMinor, err = r.czk("Hrubá dividenda / príjem"); err != nil {
		return d, err
	}
	if d.NetCZKMinor, err = r.czk("Čistá dividenda / príjem"); err != nil {
		return d, err
	}
	if d.TaxCZKMinor, err = r.czkSum("Zrazené dane", "Iné dane"); err != nil {
		return d, err
	}
	if d.FeeCZKMinor, err = r.czkSum("Poplatky"); err != nil {
		return d, err
	}
	d.IdentityKey = identityKey("dividend", d.PaidOn, d.Security.ISIN, itoa(d.GrossMinor))
	return d, nil
}

func (r investmentRow) lotSale(line int) (statement.LotSale, error) {
	l := statement.LotSale{Line: line}
	var err error
	if l.SoldOn, err = r.date("Dátum (predaja)"); err != nil {
		return l, err
	}
	if l.BoughtOn, err = r.date("Dátum (nákupu)"); err != nil {
		return l, err
	}
	const securityColumn = "Popis, symbol a ISIN"
	isin, err := r.isin(securityColumn, 1)
	if err != nil {
		return l, err
	}
	name, ticker := splitTicker(r.cell(securityColumn, 0))
	l.Security = security(isin, ticker, name)
	if l.Quantity, err = r.quantity("Units sold"); err != nil {
		return l, err
	}
	if l.ProceedsMinor, l.Currency, err = r.tradeMoney("Hodnota (predaja)"); err != nil {
		return l, err
	}
	if l.CostMinor, _, err = r.tradeMoney("Hodnota (nákupu)"); err != nil {
		return l, err
	}
	if l.GainMinor, _, err = r.tradeMoney("Kapitálové zisky"); err != nil {
		return l, err
	}
	l.CostMinor = abs(l.CostMinor)
	if l.ProceedsCZKMinor, err = r.czk("Hodnota (predaja)"); err != nil {
		return l, err
	}
	if l.CostCZKMinor, err = r.czk("Hodnota (nákupu)"); err != nil {
		return l, err
	}
	if l.CostCZKMinor != nil {
		*l.CostCZKMinor = abs(*l.CostCZKMinor)
	}
	if l.GainCZKMinor, err = r.czk("Kapitálové zisky"); err != nil {
		return l, err
	}
	if l.TaxCZKMinor, err = r.czkSum("Zrazené dane", "Iné dane"); err != nil {
		return l, err
	}
	if l.FeeCZKMinor, err = r.czkSum("Poplatky"); err != nil {
		return l, err
	}
	l.IdentityKey = identityKey("lot", l.SoldOn, l.Security.ISIN,
		l.BoughtOn.Format(time.DateOnly), l.Quantity.String(), itoa(l.ProceedsMinor))
	return l, nil
}

func (r investmentRow) buy(line int) (statement.Buy, error) {
	b := statement.Buy{Line: line}
	var err error
	if b.TradedOn, err = r.date("Dátum"); err != nil {
		return b, err
	}
	isin, err := r.isin("ISIN", 0)
	if err != nil {
		return b, err
	}
	b.Security = security(isin, r.cell("Popis a symbol", 0), "")
	if b.Quantity, err = r.quantity("Množstvo"); err != nil {
		return b, err
	}
	if b.GrossMinor, b.Currency, err = r.tradeMoney("Hrubá hodnota"); err != nil {
		return b, err
	}
	b.NetMinor, b.NetCZKMinor = b.GrossMinor, nil
	if r.cell("Čistá hodnota", tradeCurrency) != "" {
		if b.NetMinor, _, err = r.tradeMoney("Čistá hodnota"); err != nil {
			return b, err
		}
	}
	b.GrossMinor, b.NetMinor = abs(b.GrossMinor), abs(b.NetMinor)
	if b.GrossCZKMinor, err = r.czk("Hrubá hodnota"); err != nil {
		return b, err
	}
	if b.NetCZKMinor, err = r.czk("Čistá hodnota"); err != nil {
		return b, err
	}
	if b.NetCZKMinor == nil {
		b.NetCZKMinor = b.GrossCZKMinor
	}
	if b.TaxCZKMinor, err = r.czkSum("Zrazené dane", "Iné dane"); err != nil {
		return b, err
	}
	if b.FeeCZKMinor, err = r.czkSum("Poplatky"); err != nil {
		return b, err
	}
	b.IdentityKey = identityKey("buy", b.TradedOn, b.Security.ISIN, b.Quantity.String(), itoa(b.GrossMinor))
	return b, nil
}

// currencyOf reads the currency a Revolut amount is written in: "US$1.00", "1,00$", "€9", "9 CZK".
func currencyOf(raw string) (money.Currency, bool) {
	if code := currencyCode.FindString(raw); code != "" {
		return money.Currency(code), true
	}
	switch {
	case strings.Contains(raw, "$"):
		return money.USD, true
	case strings.Contains(raw, "€"):
		return money.EUR, true
	case strings.Contains(raw, "£"):
		return "GBP", true
	}
	return "", false
}

// splitTicker splits "Advanced Micro Devices AMD" into the name and the ticker, its last word.
func splitTicker(description string) (name, ticker string) {
	i := strings.LastIndex(description, " ")
	if i < 0 {
		return "", description
	}
	return strings.TrimSpace(description[:i]), description[i+1:]
}

func identityKey(kind string, day time.Time, isin string, parts ...string) string {
	return strings.Join(append([]string{kind, day.Format(time.DateOnly), isin}, parts...), "|")
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
