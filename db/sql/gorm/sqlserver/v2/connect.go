package v2

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
)

// sessionInitSQL runs on every connection before it is used, the first time and each time the
// pool hands it out again, after the server has reset the session.
//
// XACT_ABORT ON makes an error in any statement roll the whole transaction back on the
// server, as a failed statement dooms a transaction on Postgres. SQL Server's default is to
// roll back only the failed statement and carry on, so a migration or seeder that swallowed an
// error would commit the statements around it: half a schema change, recorded as done.
// go-mssqldb notices a transaction the server has ended and refuses to commit it.
const sessionInitSQL = "SET XACT_ABORT ON"

// The pool a SQL Server datasource gets for each setting its config leaves at zero.
//
// database/sql's own defaults are an unlimited pool whose connections live for ever. Against
// Azure SQL that is a pool that can exhaust the tier's session and worker limits, and with an
// Entra ID sign-in every connection it opens fetches a token. Ten open, five idle, idle ones
// closed after five minutes and every one replaced after thirty keeps a busy app within any
// tier and recycles connections across a gateway failover. There is no way to ask for an
// unlimited pool here, on purpose; set maxOpenConnections to the number you mean.
const (
	defaultMaxOpenConnections  = 10
	defaultMaxIdleConnections  = 5
	defaultConnMaxIdleLifetime = 5 * time.Minute
	defaultConnMaxLifetime     = 30 * time.Minute
)

// effectivePool fills in the defaults for the pool settings p leaves at zero, and never lets
// more connections sit idle than may be open.
func effectivePool(p dsconfig.Pool) dsconfig.Pool {
	if p.MaxOpenConnections == 0 {
		p.MaxOpenConnections = defaultMaxOpenConnections
	}
	if p.MaxIdleConnections == 0 {
		p.MaxIdleConnections = defaultMaxIdleConnections
	}
	p.MaxIdleConnections = min(p.MaxIdleConnections, p.MaxOpenConnections)
	if p.ConnectionMaxIdleLifetime == 0 {
		p.ConnectionMaxIdleLifetime = defaultConnMaxIdleLifetime
	}
	if p.ConnectionMaxLifetime == 0 {
		p.ConnectionMaxLifetime = defaultConnMaxLifetime
	}
	return p
}

func applyPool(db *sql.DB, p dsconfig.Pool) {
	db.SetMaxOpenConns(p.MaxOpenConnections)
	db.SetMaxIdleConns(p.MaxIdleConnections)
	db.SetConnMaxIdleTime(p.ConnectionMaxIdleLifetime)
	db.SetConnMaxLifetime(p.ConnectionMaxLifetime)
}

// newConnector returns the connector for dsn: go-mssqldb's for a SQL login when tokens is nil,
// and otherwise one that takes a token from tokens before each dial (see tokenFirstConnector).
// Either runs sessionInitSQL on every connection.
func newConnector(dsn string, tokens *datasourceTokens) (driver.Connector, error) {
	if tokens == nil {
		connector, err := mssql.NewConnector(dsn)
		if err != nil {
			return nil, err
		}
		connector.SessionInitSQL = sessionInitSQL
		return connector, nil
	}

	connector, err := mssql.NewConnectorWithAccessTokenProvider(dsn, tokens.loginToken)
	if err != nil {
		return nil, err
	}
	connector.SessionInitSQL = sessionInitSQL
	return &tokenFirstConnector{connector: connector, tokens: tokens}, nil
}

// tokenFirstConnector is go-mssqldb's connector for an Entra ID sign-in, asking for the token
// before it dials.
//
// go-mssqldb itself dials, completes TLS and exchanges prelogin, and only then asks for the
// token, just before it sends the login. A token that takes a while leaves that connection idle
// for as long: the first under lazy_connect with interactive or device_code waits on a person,
// MFA included, and the Azure SQL gateway closes an idle connection long before they are done.
// The login is then written to a closed socket, and the query fails with "write: broken pipe"
// though the sign-in worked. Taken first, the token is in hand when the connection opens, and
// the login follows the prelogin at once.
//
// The token rides the dial's context to the login (see loginToken), so the connection signs in
// with the token taken for it, without asking the source again.
type tokenFirstConnector struct {
	connector *mssql.Connector
	tokens    *datasourceTokens
}

func (c *tokenFirstConnector) Connect(ctx context.Context) (driver.Conn, error) {
	token, err := c.tokens.token(ctx)
	if err != nil {
		return nil, err
	}
	return c.connector.Connect(context.WithValue(ctx, takenTokenKey{}, &takenToken{token: token}))
}

func (c *tokenFirstConnector) Driver() driver.Driver { return c.connector.Driver() }

// takenTokenKey is the context key under which tokenFirstConnector hands its token to the
// login, as a *takenToken. A context prints the values it carries, a string as it is and a
// pointer to a struct as its type alone, so a logger that prints the context go-mssqldb hands
// it never prints the token.
type takenTokenKey struct{}

type takenToken struct{ token string }

// tokenTaken returns the token tokenFirstConnector took for the connection ctx opens.
func tokenTaken(ctx context.Context) (string, bool) {
	taken, ok := ctx.Value(takenTokenKey{}).(*takenToken)
	if !ok {
		return "", false
	}
	return taken.token, true
}

// loginToken is the token go-mssqldb's login asks for: the one tokenFirstConnector took before
// the dial, or a fresh one from the source should a connection arrive without it.
func (t *datasourceTokens) loginToken(ctx context.Context) (string, error) {
	if token, ok := tokenTaken(ctx); ok {
		return token, nil
	}
	return t.token(ctx)
}

// retryPolicy is how often, and how patiently, pingWithRetry tries.
type retryPolicy struct {
	// delays are the pauses between attempts; there is one attempt more than there are delays.
	delays []time.Duration
	// attemptTimeout bounds one attempt.
	attemptTimeout time.Duration
	// sleep pauses for d, or until ctx is done.
	sleep func(ctx context.Context, d time.Duration) error
}

// defaultRetry tries five times over about fifteen seconds of pauses, which covers an Azure
// SQL serverless database resuming and a gateway failing over, each of which refuses logins
// for seconds with an error that says to retry.
var defaultRetry = retryPolicy{
	delays:         []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second},
	attemptTimeout: 30 * time.Second,
	sleep:          sleepContext,
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pingWithRetry opens a connection and checks it, retrying an error that says the server is
// briefly unavailable, and returning any other at once. A login that fails, or a database
// that does not exist, fails the same way on every attempt, so retrying them would only delay
// the boot error.
func pingWithRetry(ctx context.Context, db *sql.DB, policy retryPolicy) error {
	for attempt := 0; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, policy.attemptTimeout)
		err := db.PingContext(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		if attempt >= len(policy.delays) || !isTransient(err) || ctx.Err() != nil {
			return err
		}
		if policy.sleep(ctx, policy.delays[attempt]) != nil {
			return err
		}
	}
}

// transientErrors are the SQL Server and Azure SQL error numbers that mean "try again
// shortly": a database resuming or being moved (40613, 40197, 49918–49920, 4221), and a
// service busy or at its limits (40501, 10928, 10929, 10936).
//
// They are numbers the server sends, the only ones go-mssqldb reports as an mssql.Error. The
// transport numbers the .NET client lists beside them (233, 64, 20, 121, 10053, 10054, 10060)
// are its own and Winsock's, and never arrive here; a connection the network drops arrives as
// the connection error itself (see droppedConnection).
var transientErrors = map[int32]bool{
	40613: true, 40501: true, 40197: true, 49918: true, 49919: true, 49920: true, 4221: true,
	10928: true, 10929: true, 10936: true,
}

// isTransient reports whether err says the server is briefly unavailable: one of
// transientErrors, a network timeout, or a connection dropped while it was being opened (see
// droppedConnection). Anything else — a refused connection, a failed login (18456), a
// database the login cannot open (4060, or 4063 when the server says so while falling back to
// the login's default database) — is not.
func isTransient(err error) bool {
	var serverErr mssql.Error
	if errors.As(err, &serverErr) {
		return transientErrors[serverErr.Number]
	}
	if droppedConnection(err) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// droppedConnection reports whether err is a connection the other end reset or closed while
// it was being opened: ECONNRESET, ECONNABORTED, or an EOF in the middle of the login. That is
// what an Azure SQL gateway failing over does to a connection in flight, and the next attempt
// reaches the new one. A refused connection (ECONNREFUSED) is not one: nothing is listening,
// and that is a wrong host or port as often as a server still starting.
func droppedConnection(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// warmUpGrace is how much longer than the login timeout the warm-up waits. A token source that
// bounds its own sign-in by the same timeout, as the Entra ID package's does, then fails with
// its own error, which says which setting to raise, instead of the warm-up's bare "context
// deadline exceeded".
const warmUpGrace = time.Second

// warmUp asks tokens for the first token, bounded by timeout, so that a sign-in that needs a
// person — a browser, a device code — happens at boot and once, not on whichever request
// opens the first connection. A SQL login has nothing to warm up.
func warmUp(tokens *datasourceTokens, timeout time.Duration) error {
	if tokens == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(tokens.root, timeout+warmUpGrace)
	defer cancel()
	_, err := tokens.token(ctx)
	return err
}

// errClosed is what a token request abandoned by Close reports.
var errClosed = errors.New("sqlserver: the datasource was closed")

// datasourceTokens is a datasource's TokenSource and the lifetime Close ends.
type datasourceTokens struct {
	source TokenSource
	root   context.Context
	cancel context.CancelFunc
}

// token asks the source for a token, under ctx and the datasource's lifetime both: a request
// is abandoned when the dial that made it gives up, and when the datasource is closed, even
// by a source that watches only the context it is handed.
func (t *datasourceTokens) token(ctx context.Context) (string, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(t.root, func() { cancel(errClosed) })
	defer func() {
		stop()
		cancel(nil)
	}()

	token, err := t.source.Token(ctx)
	if err != nil {
		if errors.Is(context.Cause(ctx), errClosed) {
			return "", errClosed
		}
		return "", err
	}
	if token == "" {
		return "", errors.New("sqlserver: the token source returned an empty token")
	}
	return token, nil
}

// connectError is the error a datasource that cannot connect fails with. It names the server
// and the sign-in method, and never the DSN, which carries the password.
func connectError(target, method string, err error) error {
	return fmt.Errorf("sqlserver: cannot connect to %s (auth: %s): %w", target, method, err)
}
