package migrate

import (
	"errors"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/rotisserie/eris"

	migrations "coin-book-api/db/migration"
)

func Up(connStr string) error {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return eris.Wrap(err, "open embedded migrations")
	}
	m, err := migrate.NewWithSourceInstance("iofs", source, pgxURL(connStr))
	if err != nil {
		return eris.Wrap(err, "create migrator")
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return eris.Wrap(err, "apply migrations")
	}
	return nil
}

func pgxURL(connStr string) string {
	for _, prefix := range []string{"postgresql://", "postgres://"} {
		if rest, ok := strings.CutPrefix(connStr, prefix); ok {
			return "pgx5://" + rest
		}
	}
	return connStr
}
