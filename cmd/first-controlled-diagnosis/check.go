package main

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/server"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	minJWTKeyBytes       = 32
	maxJWTKeyBytes       = 4096
	databaseCheckTimeout = 5 * time.Second
)

func checkFiles(c preparationConfig) error {
	for _, directory := range []string{c.KeyDir, c.SnapshotDir, c.StateDir} {
		if checkDirectory(directory, true) != nil {
			return errPreparation
		}
	}
	for _, directory := range []string{c.DataDir, c.SkillDir} {
		if checkDirectory(directory, false) != nil {
			return errPreparation
		}
	}
	key, err := readPrivateFile(filepath.Join(c.KeyDir, "jwt.key"), maxJWTKeyBytes)
	if err != nil {
		return errPreparation
	}
	valid := len(bytes.TrimSpace(key)) >= minJWTKeyBytes
	clear(key)
	if !valid {
		return errPreparation
	}
	resolver, err := llmpolicy.LoadProfileManifestFile(c.LocalLLMManifestPath)
	if err != nil {
		return errPreparation
	}
	if _, err = resolver.Resolve(llmpolicy.ProfileTarget{ID: c.ModelProfileID, Format: c.ModelFormat, BaseURL: c.ModelBaseURL, Model: c.ModelRef}); err != nil {
		return errPreparation
	}
	return nil
}

func checkDatabase(ctx context.Context, dsn string) error {
	if ctx == nil || ctx.Err() != nil || dsn == "" {
		return errPreparation
	}
	ctx, cancel := context.WithTimeout(ctx, databaseCheckTimeout)
	defer cancel()
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		return errPreparation
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(0)
	err = database.PingContext(ctx)
	if err == nil {
		err = server.CheckControlledReadAssemblyDatabase(ctx, database)
	}
	if closeErr := database.Close(); closeErr != nil {
		return errPreparation
	}
	if err != nil {
		return errPreparation
	}
	return nil
}
