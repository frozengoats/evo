package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Config struct {
	Directory          string
	Hostname           string
	Database           string
	AdminUsername      string
	AdminPassword      string
	Username           string
	Password           string
	AutoUpdatePassword bool
}

func (c *Config) GetAdminConnUrl(dbOverride ...string) string {
	db := c.Database
	if dbOverride != nil {
		db = dbOverride[0]
	}
	return fmt.Sprintf("postgres://%s:%s@%s/%s", c.AdminUsername, c.AdminPassword, c.Hostname, db)
}

func (c *Config) GetUserConnUrl(dbOverride ...string) string {
	db := c.Database
	if dbOverride != nil {
		db = dbOverride[0]
	}
	return fmt.Sprintf("postgres://%s:%s@%s/%s", c.Username, c.Password, c.Hostname, db)
}

type Executable interface {
	Exec(ctx context.Context, sql string, arguments ...any) (commandTag pgconn.CommandTag, err error)
}

func isHelpRequest(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}

	return false
}

func getConfig(directory string) (*Config, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("unable to access migrator directory '%s': %w", directory, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("'%s' is not a directory", directory)
	}

	database := os.Getenv("EVO_DB_DATABASE")
	if len(database) == 0 {
		return nil, fmt.Errorf("EVO_DB_DATABASE was not defined")
	}

	hostname := os.Getenv("EVO_DB_HOST")
	if len(hostname) == 0 {
		return nil, fmt.Errorf("EVO_DB_HOST was not defined")
	}

	adminUsername := os.Getenv("EVO_DB_ADMIN_USERNAME")
	if len(adminUsername) == 0 {
		return nil, fmt.Errorf("EVO_DB_ADMIN_USERNAME was not defined")
	}

	adminPassword := os.Getenv("EVO_DB_ADMIN_PASSWORD")
	if len(adminPassword) == 0 {
		return nil, fmt.Errorf("EVO_DB_ADMIN_PASSWORD was not defined")
	}

	username := os.Getenv("EVO_DB_USERNAME")
	if len(username) == 0 {
		return nil, fmt.Errorf("EVO_DB_USERNAME was not defined")
	}

	password := os.Getenv("EVO_DB_PASSWORD")
	if len(password) == 0 {
		return nil, fmt.Errorf("EVO_DB_PASSWORD was not defined")
	}

	var autoUpdatePassword bool
	autoUpdatePasswordStr := os.Getenv("EVO_AUTO_UPDATE_PASSWORD")
	if autoUpdatePasswordStr == "1" {
		autoUpdatePassword = true
	}

	return &Config{
		Directory:          directory,
		Hostname:           hostname,
		Database:           database,
		Username:           username,
		Password:           password,
		AdminUsername:      adminPassword,
		AutoUpdatePassword: autoUpdatePassword,
	}, nil
}

func printHelp() {
	fmt.Printf("usage:\nevo <directory>\n\n")
	fmt.Printf("each migrator file is treated as a go template, the environment is the dictionary\n")
	fmt.Printf("migrators are executed in ascending alphabetical order\n")
	fmt.Printf("configuration comes from the environment:\n")
	fmt.Printf("    EVO_DB_HOST              database service hostname (<host>:<port>)\n")
	fmt.Printf("    EVO_DB_ADMIN_USERNAME    database service admin username\n")
	fmt.Printf("    EVO_DB_ADMIN_PASSWORD    database service admin password\n")
	fmt.Printf("    EVO_DB_USERNAME          database service username\n")
	fmt.Printf("    EVO_DB_PASSWORD          database service password\n")
	fmt.Printf("    EVO_DB_DATABASE          database name\n")
	fmt.Printf("    EVO_AUTO_UPDATE_PASSWORD when set to 1, user password will be synced to match env value\n")
	fmt.Printf("\n")
}

func ensureUser(config *Config) error {
	var exists bool

	fmt.Printf("connecting to database '%s'\n", config.Database)
	standardConn, err := pgx.Connect(context.Background(), config.GetAdminConnUrl())
	if err != nil {
		return fmt.Errorf("unable to connect to database '%s': %w", config.Database, err)
	}
	defer func() {
		_ = standardConn.Close(context.Background())
	}()

	fmt.Printf("checking for existing user '%s'\n", config.Username)
	row := standardConn.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)", config.Username)
	err = row.Scan(&exists)
	if err != nil {
		return fmt.Errorf("unable to query database for existing user by name: %w", err)
	}

	escapedUsername, err := standardConn.PgConn().EscapeString(config.Username)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("creating user %s\n", config.Username)
		escapedPassword, err := standardConn.PgConn().EscapeString(config.Password)
		if err != nil {
			return err
		}
		_, err = standardConn.Exec(context.Background(), fmt.Sprintf("CREATE USER %s WITH PASSWORD '%s'", escapedUsername, escapedPassword))
		if err != nil {
			return fmt.Errorf("unable to create standard user '%s': %w", config.Username, err)
		}
	}

	fmt.Printf("ensuring privileges for user %s\n", config.Username)
	statements := fmt.Sprintf(strings.Join([]string{
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON TABLES TO %s;",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON SEQUENCES TO %s;",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON FUNCTIONS TO %s;",
		"GRANT CREATE ON SCHEMA public TO %s;",
	}, " "), escapedUsername, escapedUsername, escapedUsername, escapedUsername)

	_, err = standardConn.Exec(context.Background(), statements)
	if err != nil {
		return fmt.Errorf("unable to extend privileges to user '%s': %w", config.Username, err)
	}

	return nil
}

func verifyUserPassword(config *Config) (*pgx.Conn, error) {
	fmt.Printf("connecting to database '%s' as user '%s'\n", config.Database, config.Username)
	standardConn, err := pgx.Connect(context.Background(), config.GetUserConnUrl())
	if err == nil {
		return standardConn, nil
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil, err
	}

	if pgErr.Code != "28P01" {
		return nil, err
	}

	return nil, nil
}

func getPastMigrations(conn *pgx.Conn) (map[string]struct{}, error) {
	rows, err := conn.Query(context.Background(), "SELECT migrator FROM evo_mg")
	if err != nil {
		return nil, fmt.Errorf("unable to inquire for existing migrators: %w", err)
	}
	defer rows.Close()

	migrators := map[string]struct{}{}
	for rows.Next() {
		var migrator string
		// Scan the values from the current row into the struct fields
		if err := rows.Scan(&migrator); err != nil {
			return nil, fmt.Errorf("failed to read existing migrator: %w", err)
		}
		migrators[migrator] = struct{}{}
	}

	return migrators, nil
}

func ensureMigratorTable(conn *pgx.Conn) (map[string]struct{}, error) {
	fmt.Printf("checking for evo migration table\n")
	var exists bool
	row := conn.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'evo_mg')")
	err := row.Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("unable to interogate database for evo migrator table: %w", err)
	}

	if !exists {
		fmt.Printf("creating evo migration table\n")
		_, err := conn.Exec(context.Background(), "CREATE TABLE evo_mg (migrator TEXT PRIMARY KEY, created_at TIMESTAMPTZ DEFAULT NOW())")
		if err != nil {
			return nil, err
		}
	}

	return getPastMigrations(conn)
}

func executeMigrator(sql string, conn Executable, migrator string) error {
	_, err := conn.Exec(context.Background(), sql)
	if err != nil {
		return err
	}

	// after the main code has been executed, execute the migrator adjustment
	_, err = conn.Exec(context.Background(), "INSERT INTO evo_mg (migrator) VALUES ($1)", migrator)
	if err != nil {
		return err
	}

	return nil
}

func ensureLockTable(conn *pgx.Conn, lockName string) (pgx.Tx, error) {
	// create the table but drop errors if they occur, as this will result in a race condition over the name
	// index in the event of a parallel creation.  the rest of the logic below will accomplish the locking
	// needed to prevent further racing
	_, _ = conn.Exec(context.Background(), "CREATE TABLE IF NOT EXISTS evo_advisory_locks (name TEXT PRIMARY KEY)")

	_, err := conn.Exec(context.Background(), "INSERT INTO evo_advisory_locks (name) VALUES ($1) ON CONFLICT DO NOTHING", lockName)
	if err != nil {
		return nil, fmt.Errorf("unable to write advisory lock entry: %w", err)
	}

	tx, err := conn.Begin(context.Background())
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(context.Background(), "SELECT name FROM evo_advisory_locks WHERE name = $1 FOR UPDATE", lockName)
	if err != nil {
		return nil, err
	}

	return tx, nil
}

func doMigration(config *Config, preValidationHook func(config *Config)) error {
	fmt.Printf("initiating concurrency mitigation\n")
	concurrencyConn, err := pgx.Connect(context.Background(), config.GetAdminConnUrl("postgres"))
	if err != nil {
		return fmt.Errorf("unable to connect to database: %w", err)
	}
	defer func() {
		_ = concurrencyConn.Close(context.Background())
	}()

	// ensures the locking schema exists and takes out a simulated advisory lock
	tx, err := ensureLockTable(concurrencyConn, config.Database)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	fmt.Printf("connecting to postgres database\n")
	adminConn, err := pgx.Connect(context.Background(), config.GetAdminConnUrl("postgres"))
	if err != nil {
		return fmt.Errorf("unable to connect to database: %w", err)
	}
	defer func() {
		_ = adminConn.Close(context.Background())
	}()

	var exists bool

	fmt.Printf("checking if database '%s' exists\n", config.Database)
	row := adminConn.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_database WHERE datname = $1)", config.Database)
	err = row.Scan(&exists)
	if err != nil {
		return fmt.Errorf("unable to query database for existing database by name: %w", err)
	}

	if !exists {
		escapedDatabase, err := adminConn.PgConn().EscapeString(config.Database)
		if err != nil {
			return err
		}
		fmt.Printf("creating database '%s'\n", config.Database)
		_, err = adminConn.Exec(context.Background(), fmt.Sprintf("CREATE DATABASE %s WITH OWNER = DEFAULT", escapedDatabase))
		if err != nil {
			return fmt.Errorf("unable to create database '%s': %w", config.Database, err)
		}
	}

	err = ensureUser(config)
	if err != nil {
		return err
	}

	fmt.Printf("obtaining user database connection\n")
	userConn, err := verifyUserPassword(config)
	if err != nil {
		return fmt.Errorf("problem with user login: %w", err)
	}

	if userConn == nil && config.AutoUpdatePassword {
		if preValidationHook != nil {
			preValidationHook(config)
		}

		// password is bad, reset it
		escapedPassword, err := adminConn.PgConn().EscapeString(config.Password)
		if err != nil {
			return err
		}
		escapedUsername, err := adminConn.PgConn().EscapeString(config.Username)
		if err != nil {
			return err
		}
		fmt.Printf("updating password for user '%s'\n", config.Username)
		_, err = adminConn.Exec(context.Background(), fmt.Sprintf("ALTER USER %s WITH PASSWORD '%s'", escapedUsername, escapedPassword))
		if err != nil {
			return fmt.Errorf("unable update password for user '%s': %w", config.Username, err)
		}

		userConn, err = verifyUserPassword(config)
		if err != nil {
			return fmt.Errorf("problem with user login: %w", err)
		}
	}

	if userConn == nil {
		return fmt.Errorf("unable to login as user '%s'", config.Username)
	}
	defer func() {
		_ = userConn.Close(context.Background())
	}()

	existingMigrators, err := ensureMigratorTable(userConn)
	if err != nil {
		return err
	}

	globPattern := filepath.Join(config.Directory, "*.sql")
	fmt.Printf("globbing %s for migrators\n", globPattern)
	matches, err := filepath.Glob(globPattern)
	if err != nil {
		return err
	}
	sort.Slice(matches, func(i, j int) bool {
		return i < j
	})

	env := map[string]string{}
	for _, envStr := range os.Environ() {
		strParts := strings.SplitN(envStr, "=", 2)
		env[strParts[0]] = strParts[1]
	}
	for _, match := range matches {
		_, migName := filepath.Split(match)
		_, ok := existingMigrators[migName]
		if ok {
			fmt.Printf("migrator '%s' already applied...\n", migName)
			continue
		}
		fmt.Printf("executing migrator '%s'...\n", migName)
		doTransact := true
		if strings.HasSuffix(match, "_notrans.sql") {
			doTransact = false
		}

		t, err := template.ParseFiles(match)
		if err != nil {
			return fmt.Errorf("unable to parse migrator as template '%s': %w", match, err)
		}

		var buf bytes.Buffer
		err = t.Execute(&buf, env)
		if err != nil {
			return fmt.Errorf("error executing template '%s': %w", match, err)
		}

		sql := buf.String()

		if doTransact {
			tx, err := userConn.Begin(context.Background())
			if err != nil {
				return err
			}
			err = executeMigrator(sql, tx, migName)
			if err != nil {
				_ = tx.Rollback(context.Background())
				return fmt.Errorf("error executing migrator '%s' in transaction: %w", migName, err)
			}
			err = tx.Commit(context.Background())
			if err != nil {
				return fmt.Errorf("unable to commit transaction for migrator '%s': %w", migName, err)
			}
		} else {
			err = executeMigrator(sql, userConn, migName)
			if err != nil {
				return fmt.Errorf("error executing migrator '%s': %w", migName, err)
			}
		}

	}

	return nil
}

func main() {
	if len(os.Args) < 2 || isHelpRequest(os.Args) {
		printHelp()

		if isHelpRequest(os.Args) {
			os.Exit(0)
		}
		os.Exit(1)
	}

	config, err := getConfig(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err.Error())
		printHelp()
		os.Exit(1)
	}

	err = doMigration(config, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err.Error())
		os.Exit(1)
	}
}
