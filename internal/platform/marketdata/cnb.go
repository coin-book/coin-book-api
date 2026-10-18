package marketdata

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/rotisserie/eris"
	"github.com/shopspring/decimal"
)

// Rates are ČNB's rates for one day: CZK for one unit of each currency.
type Rates struct {
	Day        time.Time
	CZKPerUnit map[string]decimal.Decimal
}

// CNB reads the Czech National Bank's daily fixing (denni_kurz.txt).
type CNB struct {
	client *client
	url    string
}

func NewCNB(url, userAgent string) *CNB {
	return &CNB{client: newClient(userAgent), url: url}
}

// Latest returns the most recent fixing; on weekends and holidays that is the last business day's.
func (c *CNB) Latest(ctx context.Context) (*Rates, error) {
	res, err := c.client.get(ctx, c.url)
	if err != nil {
		return nil, eris.Wrap(err, "cnb rates")
	}
	if res.status != http.StatusOK {
		return nil, eris.Errorf("cnb rates: status %d", res.status)
	}
	return parseCNB(res.body)
}

// parseCNB reads:
//
//	17.10.2026 #201
//	země|měna|množství|kód|kurz
//	Japonsko|jen|100|JPY|15,512
func parseCNB(body []byte) (*Rates, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	if !scanner.Scan() {
		return nil, eris.New("cnb rates: empty response")
	}
	dateField, _, _ := strings.Cut(scanner.Text(), " ")
	day, err := time.Parse("02.01.2006", dateField)
	if err != nil {
		return nil, eris.Wrapf(err, "cnb rates: date %q", dateField)
	}
	rates := &Rates{Day: day, CZKPerUnit: map[string]decimal.Decimal{}}
	scanner.Scan()
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "|")
		if len(fields) != 5 {
			continue
		}
		amount, err := decimal.NewFromString(fields[2])
		if err != nil || !amount.IsPositive() {
			return nil, eris.Errorf("cnb rates: amount %q", fields[2])
		}
		rate, err := decimal.NewFromString(strings.ReplaceAll(fields[4], ",", "."))
		if err != nil || !rate.IsPositive() {
			return nil, eris.Errorf("cnb rates: rate %q", fields[4])
		}
		rates.CZKPerUnit[fields[3]] = rate.DivRound(amount, 10)
	}
	if err := scanner.Err(); err != nil {
		return nil, eris.Wrap(err, "cnb rates")
	}
	if len(rates.CZKPerUnit) == 0 {
		return nil, eris.New("cnb rates: no rates")
	}
	return rates, nil
}
