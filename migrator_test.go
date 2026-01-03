package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	AdminUsername string = "admin"
	AdminPassword string = "admin"
	Username      string = "username"
	Password      string = "password"
	Database      string = "testdb"
)

func setupDb() (*postgres.PostgresContainer, *Config, error) {
	container, err := postgres.Run(context.Background(),
		"postgres:16-alpine",
		postgres.WithUsername(AdminUsername),
		postgres.WithPassword(AdminPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, nil, err
	}

	host, err := container.Host(context.Background())
	if err != nil {
		return nil, nil, err
	}

	port, err := container.MappedPort(context.Background(), "5432/tcp")
	if err != nil {
		return nil, nil, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}

	return container, &Config{
		Hostname:           fmt.Sprintf("%s:%s", host, port.Port()),
		Database:           Database,
		AdminUsername:      AdminUsername,
		AdminPassword:      AdminPassword,
		Username:           Username,
		Password:           Password,
		Directory:          filepath.Join(cwd, "migrations"),
		AutoUpdatePassword: true,
	}, nil
}

func TestCreateDatabase(t *testing.T) {
	pgContainer, config, err := setupDb()
	assert.NoError(t, err)
	defer testcontainers.CleanupContainer(t, pgContainer)

	err = doMigration(config, func(config *Config) {
		// change the password to ensure that login fails
		config.Password = "abcdef"
	})
	assert.NoError(t, err)

	// verify that all the migrators are present
	standardConn, err := pgx.Connect(context.Background(), config.GetUserConnUrl())
	assert.NoError(t, err)
	defer func() {
		_ = standardConn.Close(context.Background())
	}()

	pastMigrations, err := getPastMigrations(standardConn)
	assert.NoError(t, err)

	assert.Contains(t, pastMigrations, "0001_make_table.sql")
	assert.Contains(t, pastMigrations, "0002_drop_and_make.sql")
	assert.Contains(t, pastMigrations, "0003_make_dtype.sql")
	assert.Contains(t, pastMigrations, "0004_edit_type_notrans.sql")
	assert.Contains(t, pastMigrations, "0005_add_index.sql")

	err = doMigration(config, nil)
	assert.NoError(t, err)
}

func TestMutlipleConcurrent(t *testing.T) {
	pgContainer, config, err := setupDb()
	assert.NoError(t, err)
	defer testcontainers.CleanupContainer(t, pgContainer)

	wg := sync.WaitGroup{}
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err = doMigration(config, nil)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
}
