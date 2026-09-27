package v2

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoolDefaultsApplyOnlyWhenUnset(t *testing.T) {
	assert.Equal(t, dsconfig.Pool{
		MaxOpenConnections:        10,
		MaxIdleConnections:        5,
		ConnectionMaxIdleLifetime: 5 * time.Minute,
		ConnectionMaxLifetime:     30 * time.Minute,
	}, effectivePool(dsconfig.Pool{}))

	set := dsconfig.Pool{
		MaxOpenConnections:        40,
		MaxIdleConnections:        20,
		ConnectionMaxIdleLifetime: time.Minute,
		ConnectionMaxLifetime:     time.Hour,
	}
	assert.Equal(t, set, effectivePool(set))
}

func TestMaxIdleNeverExceedsMaxOpen(t *testing.T) {
	assert.Equal(t, 2, effectivePool(dsconfig.Pool{MaxOpenConnections: 2}).MaxIdleConnections)
	assert.Equal(t, 3, effectivePool(dsconfig.Pool{MaxOpenConnections: 3, MaxIdleConnections: 8}).MaxIdleConnections)
	assert.Equal(t, 8, effectivePool(dsconfig.Pool{MaxIdleConnections: 8}).MaxIdleConnections,
		"an idle count within the default ten open is kept")
	assert.Equal(t, 10, effectivePool(dsconfig.Pool{MaxIdleConnections: 12}).MaxIdleConnections,
		"and one above it is cut to the ten")
}

func TestApplyPoolSetsEverySetting(t *testing.T) {
	db := sql.OpenDB(&fakeServer{})
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	applyPool(db, effectivePool(dsconfig.Pool{}))
	assert.Equal(t, 10, db.Stats().MaxOpenConnections)
}

// TestSessionInitSQLSetsXactAbort: every connection, SQL login or token, starts with
// XACT_ABORT ON, so an error dooms the transaction as it does on Postgres.
func TestSessionInitSQLSetsXactAbort(t *testing.T) {
	dsn, err := BuildDSN(sqlLogin())
	require.NoError(t, err)

	connector, err := newConnector(dsn, nil)
	require.NoError(t, err)
	assert.Equal(t, "SET XACT_ABORT ON", connector.SessionInitSQL)

	tokens := &datasourceTokens{source: staticTokens("t"), root: context.Background(), cancel: func() {}}
	connector, err = newConnector(dsn, tokens)
	require.NoError(t, err)
	assert.Equal(t, "SET XACT_ABORT ON", connector.SessionInitSQL)
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestIsTransientLoginError(t *testing.T) {
	for _, number := range []int32{40613, 40501, 40197, 49918, 49919, 49920, 4221, 10928, 10929, 10936} {
		assert.Truef(t, isTransient(mssql.Error{Number: number}), "Msg %d", number)
		assert.Truef(t, isTransient(fmt.Errorf("login: %w", mssql.Error{Number: number})), "wrapped Msg %d", number)
	}
	for _, number := range []int32{18456, 4060, 4063, 2627, 0} {
		assert.Falsef(t, isTransient(mssql.Error{Number: number}), "Msg %d fails the same way every time", number)
	}

	var timeout net.Error = timeoutError{}
	assert.True(t, isTransient(&net.OpError{Op: "dial", Err: timeout}))
	assert.True(t, isTransient(context.DeadlineExceeded), "an attempt that ran out of time")
	assert.False(t, isTransient(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}), "nothing is listening")
	assert.False(t, isTransient(errors.New("plain")))
}

// TestADroppedConnectionIsTransient: go-mssqldb reports a connection the network dropped as
// the connection's own error, never as a numbered mssql.Error — the .NET client's transport
// numbers (233, 10054, …) are not ones it sends — so a reset during an Azure gateway failover
// is recognised by the error itself, and retried.
func TestADroppedConnectionIsTransient(t *testing.T) {
	for _, err := range []error{
		&net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)},
		&net.OpError{Op: "write", Err: syscall.ECONNABORTED},
		fmt.Errorf("login error: %w", io.EOF),
		fmt.Errorf("reading prelogin: %w", io.ErrUnexpectedEOF),
	} {
		assert.Truef(t, isTransient(err), "%v", err)
	}
	for _, number := range []int32{233, 64, 20, 121, 10053, 10054, 10060} {
		assert.Falsef(t, isTransient(mssql.Error{Number: number}), "Msg %d is the .NET client's, never the server's", number)
	}

	reset := &net.OpError{Op: "read", Err: syscall.ECONNRESET}
	server := &pingServer{errs: []error{reset, fmt.Errorf("login: %w", io.EOF)}}
	var paused []time.Duration
	require.NoError(t, pingWithRetry(ctx, openPing(t, server), instantRetry(&paused)))
	assert.Equal(t, 3, server.attempts, "each dropped connection is retried")
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second}, paused)
}

// pingServer is a driver whose connections fail with errs, in turn, and then succeed.
type pingServer struct {
	mu       sync.Mutex
	errs     []error
	attempts int
}

func (p *pingServer) Connect(context.Context) (driver.Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts++
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return nil, err
	}
	return &fakeConn{server: &fakeServer{}}, nil
}

func (p *pingServer) Driver() driver.Driver            { return p }
func (p *pingServer) Open(string) (driver.Conn, error) { return p.Connect(context.Background()) }

// instantRetry is defaultRetry without the waiting, recording each pause it would take.
func instantRetry(paused *[]time.Duration) retryPolicy {
	return retryPolicy{
		delays:         defaultRetry.delays,
		attemptTimeout: time.Second,
		sleep: func(_ context.Context, d time.Duration) error {
			*paused = append(*paused, d)
			return nil
		},
	}
}

func openPing(t *testing.T, server *pingServer) *sql.DB {
	t.Helper()
	db := sql.OpenDB(server)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	return db
}

func TestPingRetriesOnlyTransientErrors(t *testing.T) {
	resuming := mssql.Error{Number: 40613, Message: "Database is not currently available"}
	server := &pingServer{errs: []error{resuming, resuming}}
	var paused []time.Duration

	require.NoError(t, pingWithRetry(ctx, openPing(t, server), instantRetry(&paused)))
	assert.Equal(t, 3, server.attempts)
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second}, paused)

	loginFailed := mssql.Error{Number: 18456, Message: "Login failed"}
	server = &pingServer{errs: []error{loginFailed}}
	paused = nil
	err := pingWithRetry(ctx, openPing(t, server), instantRetry(&paused))
	require.ErrorAs(t, err, new(mssql.Error))
	assert.Equal(t, 1, server.attempts, "a failed login fails the same way every time")
	assert.Empty(t, paused)
}

func TestPingRetryIsBounded(t *testing.T) {
	busy := mssql.Error{Number: 40501, Message: "The service is currently busy"}
	server := &pingServer{errs: []error{busy, busy, busy, busy, busy, busy, busy}}
	var paused []time.Duration

	err := pingWithRetry(ctx, openPing(t, server), instantRetry(&paused))
	require.Error(t, err)
	assert.Equal(t, 5, server.attempts)
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}, paused)
}

func TestPingStopsWhenTheContextIsDone(t *testing.T) {
	busy := mssql.Error{Number: 40501}
	server := &pingServer{errs: []error{busy, busy, busy}}
	cancelled, cancel := context.WithCancel(ctx)
	policy := retryPolicy{delays: defaultRetry.delays, attemptTimeout: time.Second, sleep: func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}}

	require.Error(t, pingWithRetry(cancelled, openPing(t, server), policy))
	assert.Equal(t, 1, server.attempts)
}

func TestSleepContextHonoursItsContext(t *testing.T) {
	require.NoError(t, sleepContext(ctx, time.Millisecond))

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	start := time.Now()
	require.ErrorIs(t, sleepContext(cancelled, time.Hour), context.Canceled)
	assert.Less(t, time.Since(start), time.Second)
}

// blockingTokens blocks every request until its context is done, and says so on entered.
type blockingTokens struct {
	entered chan struct{}
	once    sync.Once
}

func newBlockingTokens() *blockingTokens { return &blockingTokens{entered: make(chan struct{})} }

func (b *blockingTokens) Token(ctx context.Context) (string, error) {
	b.once.Do(func() { close(b.entered) })
	<-ctx.Done()
	return "", ctx.Err()
}

// TestATokenRequestEndsWithTheDatasource: a source that watches only the context it is handed
// is still abandoned when the datasource closes.
func TestATokenRequestEndsWithTheDatasource(t *testing.T) {
	source := newBlockingTokens()
	root, cancel := context.WithCancel(ctx)
	tokens := &datasourceTokens{source: source, root: root, cancel: cancel}

	done := make(chan error, 1)
	go func() {
		_, err := tokens.token(context.Background())
		done <- err
	}()
	<-source.entered
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, errClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("the token request outlived the datasource")
	}
}

func TestATokenRequestEndsWithItsDial(t *testing.T) {
	tokens := &datasourceTokens{source: newBlockingTokens(), root: context.Background(), cancel: func() {}}
	dial, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()

	_, err := tokens.token(dial)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestAnEmptyTokenIsAnError(t *testing.T) {
	tokens := &datasourceTokens{source: staticTokens(""), root: context.Background(), cancel: func() {}}
	_, err := tokens.token(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty token")

	tokens.source = staticTokens("eyJ0eXAi")
	token, err := tokens.token(ctx)
	require.NoError(t, err)
	assert.Equal(t, "eyJ0eXAi", token)
}

func TestWarmUpIsBoundedByTheLoginTimeout(t *testing.T) {
	tokens := &datasourceTokens{source: newBlockingTokens(), root: context.Background(), cancel: func() {}}

	start := time.Now()
	require.ErrorIs(t, warmUp(tokens, 20*time.Millisecond), context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.NoError(t, warmUp(nil, time.Millisecond), "a SQL login has nothing to warm up")
}
