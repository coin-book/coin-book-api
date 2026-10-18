// Package seed creates demo data for local development: a user, three months of
// synthetic statements and a small portfolio, imported through the real pipeline.
package seed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/rotisserie/eris"
	"golang.org/x/text/encoding/charmap"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/budget"
	"coin-book-api/internal/domain/categorization"
	"coin-book-api/internal/domain/identity"
	"coin-book-api/internal/domain/importing"
	"coin-book-api/internal/domain/settings"
	"coin-book-api/internal/domain/spending"
	"coin-book-api/internal/domain/transfer"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

const (
	DemoEmail    = "demo@coinbook.local"
	DemoPassword = "demo-heslo-1234" //nolint:gosec // G101: published demo credential, local development only
	demoMonths   = 3
)

const demoMBankAccount = "000019-1234567890/6210"

type movement struct {
	day         time.Time
	description string
	bankCat     string
	minor       int64
}

// Demo is idempotent: a second run finds the user and the imported files and does nothing.
func Demo(ctx context.Context, pool *db.Pool, importsDir string) error {
	sessions := identity.NewService(pool, strings.Repeat("x", 32), time.Hour, time.Hour)
	user, err := sessions.Register(ctx, DemoEmail, DemoPassword)
	if errors.Is(err, identity.ErrEmailTaken) {
		user, err = sessions.Login(ctx, DemoEmail, DemoPassword)
	}
	if err != nil {
		return err
	}
	ctx, release, err := pool.ScopeToUser(ctx, user.ID)
	if err != nil {
		return err
	}
	defer release()

	categoryRepo := categorization.NewRepo(pool)
	categorizer := categorization.NewService(pool, categoryRepo, settings.NewRepo(pool))
	importer := importing.NewService(pool, account.NewRepo(pool), categorizer, transfer.NewService(pool, categorizer), nil, importsDir)

	today := clock.Today(clock.System{})
	from := spending.MonthStart(today).AddDate(0, -(demoMonths - 1), 0)
	rng := rand.New(rand.NewPCG(42, uint64(from.Unix()))) //nolint:gosec // G404: reproducible demo data, not a secret

	mbank, revolut := demoMovements(rng, from, today)
	for name, content := range map[string][]byte{
		"demo-mbank.csv":       mbankCSV(mbank, from, today),
		"demo-revolut.csv":     revolutCSV(revolut),
		"demo-investments.csv": []byte(demoInvestmentsCSV),
	} {
		if _, err := importer.Commit(ctx, importing.Upload{FileName: name, Content: content}, false); err != nil && !errors.Is(err, importing.ErrAlreadyImported) {
			return eris.Wrapf(err, "import %s", name)
		}
	}
	return demoBudget(ctx, pool, categoryRepo, today)
}

func demoMovements(rng *rand.Rand, from, today time.Time) (mbank, revolut []movement) {
	groceries := []string{"ROHLIK.CZ", "ALBERT HM PRAHA", "LIDL DEKUJEME", "BILLA PRAHA"}
	dining := []string{"KAVARNA U TESTU", "BISTRO NA ROHU", "PIZZERIA TESTOVA"}
	for day := from; !day.After(today); day = day.AddDate(0, 0, 1) {
		switch day.Day() {
		case 10:
			mbank = append(mbank, movement{day, "VS:2026001  FIRMA TEST S.R.O.  VYPLATA  000019 2000000001 0800", "Výplata", 6200000})
		case 15:
			mbank = append(mbank, movement{day, "ODCHOZÍ PLATBA  BYTOVE DRUZSTVO TEST  NAJEM  000000 1111111111 0300", "Nájem", -1850000})
		case 18:
			mbank = append(mbank, movement{day, "INKASO  PRE ELEKTRINA  000000 2222222222 0300", "Pravidelné výdaje - jiné", -210000})
		case 20:
			mbank = append(mbank, movement{day, "ODCHOZÍ OKAMŽITÁ PLATBA  DOBITI REVOLUT", "Převod mezi vlastními účty", -500000})
			revolut = append(revolut, movement{day, "Dobitie z účtu Test", "Dobiť účet", 500000})
		case 22:
			mbank = append(mbank, movement{day, "NETFLIX.COM  PLATBA NA INTERNETU", "Hudba, filmy, knihy a časopisy", -29900})
		}
		if rng.IntN(3) == 0 {
			mbank = append(mbank, movement{day, groceries[rng.IntN(len(groceries))] + "  PLATBA KARTOU", "Potraviny a drogerie", -int64(30000 + rng.IntN(150000))})
		}
		if rng.IntN(5) == 0 {
			revolut = append(revolut, movement{day, dining[rng.IntN(len(dining))], "Predajca", -int64(9000 + rng.IntN(40000))})
		}
		if rng.IntN(9) == 0 {
			mbank = append(mbank, movement{day, "BENZINA TEST  PLATBA KARTOU", "Pohonné hmoty", -int64(120000 + rng.IntN(80000))})
		}
	}
	return mbank, revolut
}

func mbankCSV(rows []movement, from, to time.Time) []byte {
	var income, expense int64
	for _, r := range rows {
		if r.minor > 0 {
			income += r.minor
		} else {
			expense += r.minor
		}
	}
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\r\n") }
	line("mBank S.A., organizační složka")
	line("#Klient;")
	line("Demo Klient;")
	line("#Za období:;")
	line(from.Format("02.01.2006") + ";" + to.Format("02.01.2006") + ";")
	line("      #pro účty:;")
	line("      Demo Klient - " + demoMBankAccount + ";")
	line("      #Měna;#Příjmy;#Výdaje;")
	line("CZK;" + decimal(income) + ";" + decimal(expense) + ";")
	line("#Datum operace;#Popis operace;#Účet;#Kategorie;#Částka;")
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		line(fmt.Sprintf("%s;%-98s;mKonto Demo 6210 ... 7890;%s;%s CZK;;", r.day.Format(time.DateOnly), r.description, r.bankCat, decimal(r.minor)))
	}
	encoded, _ := charmap.Windows1250.NewEncoder().String(b.String())
	return []byte(encoded)
}

func revolutCSV(rows []movement) []byte {
	var b bytes.Buffer
	b.WriteString("Bežné účty Súhrny\n\nBežné účty Výpisy o transakciách\n\nOsobný účet (CZK)\n\nVýpis transakcií\n")
	b.WriteString("Dátum,Popis,Kategória,Pohyb peňazí,Zostatok,Zrazená daň,Iné dane,Poplatky\n")
	var balance, sum int64 = 200000, 0
	for _, r := range rows {
		balance += r.minor
		sum += r.minor
		fmt.Fprintf(&b, "%d. %d. %d,%s,%s,\"%s CZK\",\"%s CZK\",\"0,00 CZK\",\"0,00 CZK\",\"0,00 CZK\"\n",
			r.day.Day(), int(r.day.Month()), r.day.Year(), r.description, r.bankCat, decimal(r.minor), decimal(balance))
	}
	fmt.Fprintf(&b, "Spolu,,,\"%s CZK\",,,,\n---------\n", decimal(sum))
	return b.Bytes()
}

// demoInvestmentsCSV has fixed dates, so a daily restart finds it already imported: one
// lot held over three years, a recent buy, a sale from each, and monthly ETF purchases.
const demoInvestmentsCSV = `Bežné účty Súhrny
---------
Investment Services Výpisy o transakciách
Výpis transakcií (len potvrdenie o prijatí dividend)
Dátum,Popis a symbol,ISIN,Krajina,Hrubá dividenda / príjem,Hrubá dividenda / príjem,Zrazené dane,Zrazené dane,Iné dane,Iné dane,Poplatky,Poplatky,Čistá dividenda / príjem,Čistá dividenda / príjem
14. 8. 2026,Apple dividend,US0378331005,US,"1,30$","27,30 CZK","0,20$","4,20 CZK","0,00$","0,00 CZK","0,00 CZK","0,00 CZK","1,10$","23,10 CZK"
Predané jednotky
Dátum (predaja),Dátum (nákupu),"Popis, symbol a ISIN","Popis, symbol a ISIN",Krajina,Vek jednotiek,Units sold,Cena za jednotku (dátum začiatku výpredaja),Cena za jednotku (dátum zakúpenia),Cena za jednotku (dátum začiatku výpredaja),Cena za jednotku (dátum zakúpenia),Hodnota (predaja),Hodnota (nákupu),Hodnota (predaja),Hodnota (nákupu),Kapitálové zisky,Kapitálové zisky,Zrazené dane,Zrazené dane,Iné dane,Iné dane,Poplatky,Poplatky
3. 3. 2026,2. 3. 2022,Apple Inc AAPL,US0378331005,US,1462 days,2,+US$250,-US$165.50,"+5,750 CZK","-3,720.44 CZK",+US$500,-US$331,"+11,500 CZK","-7,440.88 CZK",US$169,"3,859.12 CZK",US$0,0 CZK,US$0,0 CZK,US$0,0 CZK
3. 3. 2026,15. 1. 2026,Apple Inc AAPL,US0378331005,US,47 days,1,+US$250,-US$260,"+5,750 CZK","-5,460 CZK",+US$250,-US$260,"+5,750 CZK","-5,460 CZK",-US$10,290 CZK,US$0,0 CZK,US$0,0 CZK,US$0,0 CZK
Iné transakcie na brokerskom účte
Dátum,Popis a symbol,ISIN,Množstvo,Krajina,Hrubá hodnota,Hrubá hodnota,Zrazené dane,Zrazené dane,Iné dane,Iné dane,Poplatky,Poplatky,Čistá hodnota,Čistá hodnota
2. 3. 2022,AAPL,US0378331005,10,United States,US$1655,"37,202.20 CZK",US$0,0 CZK,US$0,0 CZK,US$0,0 CZK,US$1655,"37,202.20 CZK"
15. 1. 2026,AAPL,US0378331005,4,United States,US$1040,"21,840 CZK",US$0,0 CZK,US$0,0 CZK,US$1,21 CZK,US$1041,"21,861 CZK"
Robo-Advisor
Iné transakcie na brokerskom účte
Dátum,Popis a symbol,ISIN,Množstvo,Krajina,Hrubá hodnota,Hrubá hodnota,Zrazené dane,Zrazené dane,Iné dane,Iné dane,Poplatky,Poplatky,Čistá hodnota,Čistá hodnota
3. 2. 2026,EUNL,IE00B4L5Y983,1.81818182,Ireland,€200.00,"4,860.00 CZK",€0,0 CZK,€0,0 CZK,€0,0 CZK,€200.00,"4,860.00 CZK"
3. 3. 2026,EUNL,IE00B4L5Y983,1.78571429,Ireland,€200.00,"4,850.00 CZK",€0,0 CZK,€0,0 CZK,€0,0 CZK,€200.00,"4,850.00 CZK"
3. 4. 2026,EUNL,IE00B4L5Y983,1.75438596,Ireland,€200.00,"4,870.00 CZK",€0,0 CZK,€0,0 CZK,€0,0 CZK,€200.00,"4,870.00 CZK"
4. 5. 2026,EUNL,IE00B4L5Y983,1.69491525,Ireland,€200.00,"4,880.00 CZK",€0,0 CZK,€0,0 CZK,€0,0 CZK,€200.00,"4,880.00 CZK"
---------
`

func decimal(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d,%02d", sign, minor/100, minor%100)
}

func demoBudget(ctx context.Context, pool *db.Pool, categories *categorization.Repo, today time.Time) error {
	var planned bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM budget WHERE month = $1)`, spending.MonthStart(today)).Scan(&planned); err != nil || planned {
		return eris.Wrap(err, "check demo budget")
	}
	cats, err := categories.Categories(ctx, nil)
	if err != nil {
		return err
	}
	byCode := map[string]categorization.Category{}
	for _, c := range cats {
		byCode[c.Code] = c
	}
	plan := map[string]int64{"housing": 2200000, "groceries": 900000, "dining": 300000, "transport": 250000, "leisure": 150000}
	var lines []budget.Line
	for code, planned := range plan {
		id := byCode[code].ID
		lines = append(lines, budget.Line{CategoryID: &id, PlannedMinor: planned})
	}
	svc := budget.NewService(pool, spending.NewRepo(pool), categories, clock.System{})
	return svc.Replace(ctx, today, money.CZK, lines)
}
