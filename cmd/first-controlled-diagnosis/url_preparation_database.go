package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/Autumn-27/artex/server"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// 검증한 DSN bytes에서 파싱한 설정 자체를 연결에 사용한다. DNS 및 fallback 자동확장은 허용하지 않는다.
func urlOpenDatabase(ctx context.Context, installed urlPreparationInstallation, expected urlInputPin) (*sql.DB, error) {
	raw, pin, err := urlReadPin(expected.path, 16<<10, false)
	defer clear(raw)
	if err != nil || pin.digest != expected.digest || !os.SameFile(pin.info, expected.info) {
		return nil, errPreparation
	}
	dsn := string(bytes.TrimSpace(raw))
	if dsn == "" || strings.ContainsAny(dsn, "\r\n\x00") {
		return nil, errPreparation
	}
	config, err := urlParseDatabase(dsn)
	if err != nil || !urlDatabaseProtected(installed, config) {
		return nil, errPreparation
	}
	pool := stdlib.OpenDB(*config)
	pool.SetMaxOpenConns(8)
	pool.SetMaxIdleConns(2)
	call, cancel := context.WithTimeout(ctx, databaseCheckTimeout)
	defer cancel()
	if pool.PingContext(call) != nil || expected.check() != nil {
		_ = pool.Close()
		return nil, errPreparation
	}
	return pool, nil
}
func (l *urlLoadedProfile) databasePin() (urlInputPin, error) {
	for _, pin := range l.pins {
		if pin.path == l.launch.RuntimeDSNFile {
			return pin, nil
		}
	}
	return urlInputPin{}, errPreparation
}
func (b *urlPreparationRuntime) openURLReport(ctx context.Context, l *urlLoadedProfile, task int64) (*savedReportService, error) {
	pin, err := l.databasePin()
	if err != nil {
		return nil, errPreparation
	}
	pool, err := urlOpenDatabase(ctx, b.config, pin)
	if err != nil {
		return nil, errPreparation
	}
	handler, err := server.NewFirstDiagnosisReportHandler(ctx, pool, l.launch.KeyDir, l.launch.DataDir, task)
	if err != nil {
		_ = pool.Close()
		return nil, errPreparation
	}
	return &savedReportService{handler: handler, close: pool.Close}, nil
}

var urlDatabaseKeys = strings.Fields("host port user password dbname sslmode connect_timeout application_name search_path options statement_timeout lock_timeout idle_in_transaction_session_timeout timezone client_encoding target_session_attrs")

// 파일·환경에서 DB identity를 다시 선택하는 libpq 간접 설정은 새 URL 모드에서 지원하지 않는다.
// 값은 기록하지 않으며 실제 문법 검증은 pgx allow-list parser가 담당한다.
func urlDatabaseExplicitKeys(dsn string) (map[string]bool, error) {
	for i := 0; i < len(dsn); i++ {
		if dsn[i] <= 0x1f || dsn[i] == 0x7f {
			return nil, errPreparation
		}
	}
	keys := map[string]bool{}
	for i := 0; i < len(dsn); {
		for i < len(dsn) && dsn[i] == ' ' {
			i++
		}
		if i == len(dsn) {
			break
		}
		start := i
		for i < len(dsn) && ((dsn[i] >= 'a' && dsn[i] <= 'z') || dsn[i] == '_') {
			i++
		}
		if start == i || i == len(dsn) || dsn[i] != '=' {
			return nil, errPreparation
		}
		key := dsn[start:i]
		if keys[key] {
			return nil, errPreparation
		}
		keys[key] = true
		i++
		if i < len(dsn) && dsn[i] == '\'' {
			i++
			closed := false
			for i < len(dsn) {
				if dsn[i] == '\\' {
					i += 2
					continue
				}
				if dsn[i] == '\'' {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed || i > len(dsn) || i < len(dsn) && dsn[i] != ' ' {
				return nil, errPreparation
			}
		} else {
			for i < len(dsn) && dsn[i] != ' ' {
				if dsn[i] == '\\' {
					i += 2
				} else {
					i++
				}
			}
			if i > len(dsn) {
				return nil, errPreparation
			}
		}
	}
	for _, key := range []string{"host", "port", "user", "password", "dbname", "sslmode"} {
		if !keys[key] {
			return nil, errPreparation
		}
	}
	return keys, nil
}
func urlParseDatabase(dsn string) (*pgx.ConnConfig, error) {
	if _, err := urlDatabaseExplicitKeys(dsn); err != nil {
		return nil, errPreparation
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PG") {
			return nil, errPreparation
		}
	}
	account, err := user.Current()
	if err != nil {
		return nil, errPreparation
	}
	// pgx가 환경 변수 없이 자동 검색하는 기본 인증 파일도 지원하지 않는다.
	for _, relative := range []string{".pgpass", ".postgresql/postgresql.crt", ".postgresql/postgresql.key", ".postgresql/root.crt"} {
		_, err := os.Lstat(filepath.Join(account.HomeDir, relative))
		if err == nil || !os.IsNotExist(err) {
			return nil, errPreparation
		}
	}
	config, err := pgx.ParseConfigWithOptions(dsn, pgx.ParseConfigOptions{ParseConfigOptions: pgconn.ParseConfigOptions{ConnStringAllowedKeys: urlDatabaseKeys}})
	if err != nil || config.Host == "" || config.Port == 0 || config.User == "" || config.Database == "" {
		return nil, errPreparation
	}
	return config, nil
}
