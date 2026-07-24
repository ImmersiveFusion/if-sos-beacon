package store

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/config"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

func TestResolveDSN(t *testing.T) {
	tests := []struct {
		name    string
		env     string // SOS_STORE_DSN; "" means unset
		cfg     config.StoreConfig
		want    string
		wantErr bool
	}{
		{
			name: "env override wins",
			env:  "sqlserver://env-host?database=d",
			cfg:  config.StoreConfig{DSN: "sqlserver://cfg-host", Server: "s", Database: "d"},
			want: "sqlserver://env-host?database=d",
		},
		{
			name: "explicit dsn when no env",
			cfg:  config.StoreConfig{DSN: "sqlserver://cfg-host?database=d"},
			want: "sqlserver://cfg-host?database=d",
		},
		{
			name: "compose fedauth from server + database",
			cfg:  config.StoreConfig{Server: "my.database.windows.net", Database: "beacon"},
			want: "server=my.database.windows.net;database=beacon;fedauth=ActiveDirectoryDefault",
		},
		{
			name:    "error when nothing provided",
			cfg:     config.StoreConfig{},
			wantErr: true,
		},
		{
			name:    "error when only server provided",
			cfg:     config.StoreConfig{Server: "s"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Isolate the env var for every case (empty string counts as unset).
			t.Setenv(storeDSNEnv, tc.env)
			got, err := resolveDSN(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveDSN() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveDSN: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolveDSN() = %q, want %q", got, tc.want)
			}
		})
	}
}

func newMock(t *testing.T) (*AzureSQL, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &AzureSQL{db: db}, mock
}

func TestAzureSQL_Seen(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		s, mock := newMock(t)
		mock.ExpectQuery(sqlSeenSelect).WithArgs("hn", "1").
			WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))
		got, err := s.Seen("hn", "1")
		if err != nil || !got {
			t.Fatalf("Seen = %v, %v; want true, nil", got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("absent", func(t *testing.T) {
		s, mock := newMock(t)
		mock.ExpectQuery(sqlSeenSelect).WithArgs("hn", "2").
			WillReturnRows(sqlmock.NewRows([]string{"one"}))
		got, err := s.Seen("hn", "2")
		if err != nil || got {
			t.Fatalf("Seen = %v, %v; want false, nil", got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
}

func TestAzureSQL_MarkSeen(t *testing.T) {
	s, mock := newMock(t)
	mock.ExpectExec(sqlSeenInsert).WithArgs("hn", "1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := s.MarkSeen("hn", "1"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestAzureSQL_Record(t *testing.T) {
	s, mock := newMock(t)
	f := core.Finding{
		Beacon:  "sos-apm",
		Signal:  core.Signal{Source: "hn", ID: "42"},
		Verdict: core.Verdict{Bucket: "seeker", Fit: 0.9},
	}
	mock.ExpectBegin()
	mock.ExpectExec(sqlFindingUpsert).
		WithArgs("sos-apm", "hn", "42", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(sqlSeenInsert).
		WithArgs("hn", "42").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := s.Record(f); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestAzureSQL_Claim(t *testing.T) {
	s, mock := newMock(t)
	mock.ExpectExec(sqlClaimUpsert).WithArgs("hn:42", "alice").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := s.Claim("hn:42", "alice"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestAzureSQL_PendingDigest(t *testing.T) {
	s, mock := newMock(t)
	want := core.Finding{
		Beacon:  "sos-apm",
		Signal:  core.Signal{Source: "hn", ID: "42", Title: "Datadog bill"},
		Verdict: core.Verdict{Bucket: "incumbent-rage", Fit: 0.91},
	}
	data, _ := json.Marshal(want)
	mock.ExpectQuery(sqlPendingSelect).WithArgs("sos-apm").
		WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow(string(data)))

	got, err := s.PendingDigest("sos-apm")
	if err != nil {
		t.Fatalf("PendingDigest: %v", err)
	}
	if len(got) != 1 || got[0].Signal.ID != "42" || got[0].Verdict.Bucket != "incumbent-rage" {
		t.Fatalf("PendingDigest = %+v, want the recorded finding", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// TestAzureSQL_Live exercises a real database end to end. It is skipped unless
// SOS_AZURESQL_TEST_DSN is set, so CI never needs a database. Point it at a
// throwaway Azure SQL / SQL Server database:
//
//	SOS_AZURESQL_TEST_DSN='sqlserver://user:pass@host?database=db' go test ./internal/store -run Live
func TestAzureSQL_Live(t *testing.T) {
	dsn := os.Getenv("SOS_AZURESQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set SOS_AZURESQL_TEST_DSN to run the live Azure SQL integration test")
	}
	// Do not let a stray SOS_STORE_DSN override the test DSN.
	t.Setenv(storeDSNEnv, "")

	s, err := OpenAzureSQL(config.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("OpenAzureSQL: %v", err)
	}
	defer func() { _ = s.Close() }()

	src, id := "test", "live-"+t.Name()
	seen, err := s.Seen(src, id)
	if err != nil {
		t.Fatalf("Seen: %v", err)
	}
	if !seen {
		if err := s.MarkSeen(src, id); err != nil {
			t.Fatalf("MarkSeen: %v", err)
		}
	}
	if seen, err = s.Seen(src, id); err != nil || !seen {
		t.Fatalf("Seen after MarkSeen = %v, %v; want true, nil", seen, err)
	}

	f := core.Finding{
		Beacon:  "live-beacon",
		Signal:  core.Signal{Source: src, ID: id + "-f", Title: "live"},
		Verdict: core.Verdict{Bucket: "seeker", Fit: 0.8},
	}
	if err := s.Record(f); err != nil {
		t.Fatalf("Record: %v", err)
	}
	pending, err := s.PendingDigest("live-beacon")
	if err != nil {
		t.Fatalf("PendingDigest: %v", err)
	}
	if len(pending) == 0 {
		t.Error("PendingDigest returned nothing after Record")
	}
	if err := s.Claim(f.Signal.Source+":"+f.Signal.ID, "tester"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
}
