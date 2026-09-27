package azuread

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The datasource end to end: the engine's constructor, this package's authenticator, the token
// source and go-mssqldb's connector, against a server that speaks just enough TDS to take a
// login and refuse it. The login it refuses carries the token the connection signed in with, so
// the tests see which token each connection used, and how often the credential was asked.

// fakeTDS accepts connections on 127.0.0.1 over TLS from the first byte, as TDS 8.0 (ssl:
// strict) has it, with a certificate made for the test. It records the LOGIN7 message it
// decrypts and every byte it read off the wire, and refuses the login with error 18456, which
// the engine does not retry. A token method refuses weaker encryption, so the fake needs TLS to
// be reached at all, and the wire it records shows the token never crossed it in clear.
type fakeTDS struct {
	listener net.Listener
	tls      *tls.Config
	// certPath is the server's certificate as a PEM file, which options.certificate verifies
	// the connection against.
	certPath string
	// idle, when set, is how long the fake waits for the login after its prelogin reply before it
	// closes the connection, as the Azure SQL gateway closes one that sits idle.
	idle time.Duration
	wg   sync.WaitGroup

	mu         sync.Mutex
	logins     [][]byte
	wire       []byte
	idleClosed int
}

func startFakeTDS(t *testing.T) *fakeTDS { return startGateway(t, 0) }

// startGateway is startFakeTDS closing each connection that sends no login within idle of the
// prelogin reply.
func startGateway(t *testing.T, idle time.Duration) *fakeTDS {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	cert, certPEM := newServerCertificate(t)
	server := &fakeTDS{
		listener: listener,
		tls:      &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"tds/8.0"}},
		certPath: writeFile(t, "server.pem", certPEM),
		idle:     idle,
	}
	server.wg.Add(1)
	go server.serve()
	t.Cleanup(func() {
		assert.NoError(t, listener.Close())
		server.wg.Wait()
	})
	return server
}

func (f *fakeTDS) port() int { return f.listener.Addr().(*net.TCPAddr).Port }

func (f *fakeTDS) serve() {
	defer f.wg.Done()
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			f.handle(conn)
		}()
	}
}

// The TDS packet types and prelogin options the fake needs (MS-TDS 2.2.3.1.1, 2.2.6.5).
const (
	tdsReply    = 0x04
	tdsLogin7   = 0x10
	tdsPrelogin = 0x12

	preloginVersion    = 0x00
	preloginEncryption = 0x01
	preloginFedAuth    = 0x06
	encryptOn          = 0x01
)

// newServerCertificate is a self-signed certificate for 127.0.0.1, and the certificate alone as
// PEM. It is made here rather than committed, as the service principal's are.
func newServerCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gorgany test TDS server"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// recordingConn is a connection that hands what it reads to record as well.
type recordingConn struct {
	net.Conn
	record func([]byte)
}

func (r recordingConn) Read(p []byte) (int, error) {
	n, err := r.Conn.Read(p)
	r.record(p[:n])
	return n, err
}

func (f *fakeTDS) handle(raw net.Conn) {
	defer func() { _ = raw.Close() }()
	_ = raw.SetDeadline(time.Now().Add(30 * time.Second))

	conn := tls.Server(recordingConn{Conn: raw, record: func(read []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.wire = append(f.wire, read...)
	}}, f.tls)
	if conn.Handshake() != nil {
		return
	}
	if kind, _, err := readTDS(conn); err != nil || kind != tdsPrelogin {
		return
	}
	if writeTDS(conn, tdsReply, preloginReply()) != nil {
		return
	}
	if f.idle > 0 {
		_ = raw.SetReadDeadline(time.Now().Add(f.idle))
	}
	kind, login, err := readTDS(conn)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		f.mu.Lock()
		f.idleClosed++
		f.mu.Unlock()
	}
	if err != nil || kind != tdsLogin7 {
		return
	}
	f.mu.Lock()
	f.logins = append(f.logins, login)
	f.mu.Unlock()
	_ = writeTDS(conn, tdsReply, loginFailed())
}

func (f *fakeTDS) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.logins)
}

// idleCloses is how many connections the fake closed for sending no login within idle.
func (f *fakeTDS) idleCloses() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.idleClosed
}

// wireCarries reports whether token crossed the wire in clear, and how many bytes the wire
// carried, so that an answer of no is known to have looked at something.
func (f *fakeTDS) wireCarries(token string) (bool, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return bytes.Contains(f.wire, ucs2(token)) || bytes.Contains(f.wire, []byte(token)), len(f.wire)
}

// loginTokens reports, for each login received so far, whether it carried token.
func (f *fakeTDS) loginTokens(token string) []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	carried := make([]bool, len(f.logins))
	for i, login := range f.logins {
		carried[i] = bytes.Contains(login, ucs2(token))
	}
	return carried
}

// readTDS reads one message, which may span packets, and returns its type and payload.
func readTDS(r io.Reader) (byte, []byte, error) {
	var kind byte
	var payload []byte
	for {
		header := make([]byte, 8)
		if _, err := io.ReadFull(r, header); err != nil {
			return 0, nil, err
		}
		kind = header[0]
		body := make([]byte, int(binary.BigEndian.Uint16(header[2:4]))-len(header))
		if _, err := io.ReadFull(r, body); err != nil {
			return 0, nil, err
		}
		payload = append(payload, body...)
		if header[1]&0x01 != 0 { // end of message
			return kind, payload, nil
		}
	}
}

func writeTDS(w io.Writer, kind byte, payload []byte) error {
	header := []byte{kind, 0x01, 0, 0, 0, 0, 1, 0}
	binary.BigEndian.PutUint16(header[2:4], uint16(len(header)+len(payload)))
	_, err := w.Write(append(header, payload...))
	return err
}

// preloginReply says encryption is on, as it is from the first byte, and that the server takes
// federated authentication.
func preloginReply() []byte {
	type option struct {
		token byte
		value []byte
	}
	options := []option{
		{preloginVersion, []byte{16, 0, 0x07, 0xd0, 0, 0}},
		{preloginEncryption, []byte{encryptOn}},
		{preloginFedAuth, []byte{1}},
	}
	offset := len(options)*5 + 1
	var table, data []byte
	for _, o := range options {
		table = append(table, o.token)
		table = binary.BigEndian.AppendUint16(table, uint16(offset+len(data)))
		table = binary.BigEndian.AppendUint16(table, uint16(len(o.value)))
		data = append(data, o.value...)
	}
	return append(append(table, 0xff), data...)
}

// loginFailed is an ERROR token for 18456 and the DONE token that ends the reply with an error.
func loginFailed() []byte {
	message := ucs2("Login failed for user '<token-identified principal>'.")
	server := ucs2("fake")

	var body []byte
	body = binary.LittleEndian.AppendUint32(body, 18456)
	body = append(body, 1, 14) // state, class
	body = binary.LittleEndian.AppendUint16(body, uint16(len(message)/2))
	body = append(body, message...)
	body = append(body, byte(len(server)/2))
	body = append(body, server...)
	body = append(body, 0) // no procedure
	body = binary.LittleEndian.AppendUint32(body, 1)

	reply := []byte{0xaa}
	reply = binary.LittleEndian.AppendUint16(reply, uint16(len(body)))
	reply = append(reply, body...)
	reply = append(reply, 0xfd)                           // DONE
	reply = binary.LittleEndian.AppendUint16(reply, 0x02) // DONE_ERROR
	reply = binary.LittleEndian.AppendUint16(reply, 0)
	return binary.LittleEndian.AppendUint64(reply, 0)
}

func ucs2(s string) []byte {
	var out []byte
	for _, unit := range utf16.Encode([]rune(s)) {
		out = binary.LittleEndian.AppendUint16(out, unit)
	}
	return out
}

// useCredential makes every datasource built for the rest of t sign in with cred, and records
// the requests the engine made.
func useCredential(t *testing.T, cred azcore.TokenCredential) *[]sqlserver.AuthRequest {
	t.Helper()
	var requests []sqlserver.AuthRequest
	saved := credentialFor
	credentialFor = func(req sqlserver.AuthRequest, remembered *rememberedSignIn) (azcore.TokenCredential, error) {
		requests = append(requests, req)
		return cred, nil
	}
	t.Cleanup(func() { credentialFor = saved })
	return &requests
}

// interactiveAt is a datasource config that signs in interactively to server, verifying its
// certificate.
func interactiveAt(server *fakeTDS) dsconfig.DataSource {
	return dsconfig.DataSource{
		Driver:   "sqlserver_gorm",
		Host:     "127.0.0.1",
		Port:     server.port(),
		Database: "Example-db",
		Username: standInUser,
		SSL:      "strict",
		Options:  map[string]string{"certificate": server.certPath},
		Auth:     dsconfig.Auth{Method: sqlserver.AuthMethodInteractive},
	}
}

func sqlDBOf(t *testing.T, ds dbCore.IDataSource) *sql.DB {
	t.Helper()
	handle, err := ds.GetDriver()
	require.NoError(t, err)
	db, err := handle.(*gorm.DB).DB()
	require.NoError(t, err)
	return db
}

// TestTheDatasourceSignsInOnceAtWarmUp: the constructor signs the person in before it dials, and
// the connection it then opens signs in with that token, without asking the credential again.
func TestTheDatasourceSignsInOnceAtWarmUp(t *testing.T) {
	server := startFakeTDS(t)
	c := newClock()
	cred := &fakeCredential{clock: c}
	requests := useCredential(t, cred)

	_, err := sqlserver.NewDataSourceWithConfig(interactiveAt(server))
	require.Error(t, err, "the fake server refuses every login")
	assert.Contains(t, err.Error(), "(auth: interactive)")
	assert.Contains(t, err.Error(), "Login failed")
	assert.NotContains(t, err.Error(), "token-1")

	getTokens, authenticates := cred.counts()
	assert.Equal(t, 1, authenticates, "one sign-in")
	assert.Equal(t, 1, getTokens, "and one token, which the connection reused")
	assert.Equal(t, []bool{true}, server.loginTokens("token-1"), "the login carried the warm-up's token")
	inClear, wire := server.wireCarries("token-1")
	assert.NotZero(t, wire)
	assert.False(t, inClear, "the token never crossed the wire in clear")

	require.Len(t, *requests, 1, "one credential for the datasource")
	req := (*requests)[0]
	assert.Equal(t, standInUser, req.Username)
	assert.Equal(t, sqlserver.DefaultInteractiveLoginTimeout, req.LoginTimeout)
	assert.Error(t, req.Context.Err(), "the failed constructor closed the datasource's lifetime")
}

// TestLazyConnectDefersTheSignInToTheFirstConnection: with lazy_connect the constructor asks
// nobody, the first connection signs the person in, and the next reuses the token.
func TestLazyConnectDefersTheSignInToTheFirstConnection(t *testing.T) {
	server := startFakeTDS(t)
	c := newClock()
	cred := &fakeCredential{clock: c}
	useCredential(t, cred)

	cfg := interactiveAt(server)
	cfg.LazyConnect = true
	ds, err := driver.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	getTokens, authenticates := cred.counts()
	assert.Zero(t, authenticates, "the constructor signed nobody in")
	assert.Zero(t, getTokens)

	db := sqlDBOf(t, ds)
	for range 2 {
		err := db.PingContext(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Login failed")
	}
	getTokens, authenticates = cred.counts()
	assert.Equal(t, 1, authenticates, "the first connection signed in")
	assert.Equal(t, 1, getTokens)
	assert.Equal(t, []bool{true, true}, server.loginTokens("token-1"), "both connections used the one token")
}

// TestASlowSignInDoesNotStrandTheConnection: the first connection under lazy_connect signs the
// person in, which takes as long as they take, MFA included. Had it dialled first, the
// connection would sit idle through the sign-in, the gateway would close it, and the login would
// be written to a closed socket: "write: broken pipe". It signs in first and then dials, so the
// login follows the prelogin at once.
func TestASlowSignInDoesNotStrandTheConnection(t *testing.T) {
	const idle = 200 * time.Millisecond
	server := startGateway(t, idle)
	cred := &fakeCredential{clock: newClock(), authenticate: func(ctx context.Context) error {
		select {
		case <-time.After(5 * idle):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	useCredential(t, cred)

	cfg := interactiveAt(server)
	cfg.LazyConnect = true
	ds, err := sqlserver.NewDataSourceWithConfig(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	err = sqlDBOf(t, ds).PingContext(context.Background())
	require.Error(t, err, "the fake server refuses every login")
	assert.Contains(t, err.Error(), "Login failed", "the login reached a connection still open")
	assert.Zero(t, server.idleCloses(), "no connection sat idle through the sign-in")
	assert.Equal(t, []bool{true}, server.loginTokens("token-1"), "one connection, with the sign-in's token")
}

// TestCloseAbandonsALazySignInInFlight: a lazy interactive sign-in waits on a person under a
// query's context, which is often context.Background(). Close must return at once and nil, and
// the query must fail rather than hang.
func TestCloseAbandonsALazySignInInFlight(t *testing.T) {
	server := startFakeTDS(t)
	entered := make(chan struct{})
	cred := &fakeCredential{clock: newClock(), authenticate: func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}}
	useCredential(t, cred)

	cfg := interactiveAt(server)
	cfg.LazyConnect = true
	ds, err := sqlserver.NewDataSourceWithConfig(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	// Resolved here: sqlDBOf fails the test, which only the test's own goroutine may do.
	db := sqlDBOf(t, ds)
	pinged := make(chan error, 1)
	go func() { pinged <- db.PingContext(context.Background()) }()
	waitFor(t, entered, "the sign-in")

	closed := make(chan error, 1)
	go func() { closed <- ds.Close() }()
	require.NoError(t, waitFor(t, closed, "Close"))

	err = waitFor(t, pinged, "the abandoned query's return")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
	assert.Zero(t, server.loginCount(), "no login was sent without a token")
	require.NoError(t, ds.Close(), "closing again is harmless")
}

// TestEachDatasourceSignsInOnItsOwn: the one sign-in is per datasource. Two datasources get two
// credentials, and with interactive a person signs in for each, the second at its own boot, or
// at its first query with lazy_connect.
func TestEachDatasourceSignsInOnItsOwn(t *testing.T) {
	server := startFakeTDS(t)
	cred := &fakeCredential{clock: newClock()}
	requests := useCredential(t, cred)

	for range 2 {
		_, err := sqlserver.NewDataSourceWithConfig(interactiveAt(server))
		require.Error(t, err, "the fake server refuses every login")
	}
	require.Len(t, *requests, 2, "a credential for each datasource")
	_, authenticates := cred.counts()
	assert.Equal(t, 2, authenticates, "and a sign-in for each")
}

// TestASovereignHostReachesTheCredentialWithItsCloudAndScope: the engine derives both from the
// host, and this package builds the credential from what it is handed.
func TestASovereignHostReachesTheCredentialWithItsCloudAndScope(t *testing.T) {
	requests := useCredential(t, &fakeCredential{clock: newClock()})

	ds, err := sqlserver.NewDataSourceWithConfig(dsconfig.DataSource{
		Driver:      "sqlserver_gorm",
		Host:        usGovHost,
		Port:        1433,
		Database:    "Example-db",
		LazyConnect: true,
		Auth:        dsconfig.Auth{Method: sqlserver.AuthMethodAzureCLI, LoginTimeout: 90 * time.Second},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	require.Len(t, *requests, 1)
	req := (*requests)[0]
	assert.Equal(t, dsconfig.AzureCloudUSGov, req.Cloud)
	assert.Equal(t, "https://database.usgovcloudapi.net/.default", req.Scope)
	assert.Equal(t, 90*time.Second, req.LoginTimeout)
}

// TestTheEngineRefusesWhatAMethodCannotHonourBeforeThisPackageRuns: the field rules are the
// engine's, and a refused config never builds a credential.
func TestTheEngineRefusesWhatAMethodCannotHonourBeforeThisPackageRuns(t *testing.T) {
	requests := useCredential(t, &fakeCredential{clock: newClock()})

	cfg := dsconfig.DataSource{
		Driver: "sqlserver_gorm", Host: azureHost, Port: 1433, Database: "Example-db", LazyConnect: true,
		Password: standInSecret,
		Auth:     dsconfig.Auth{Method: sqlserver.AuthMethodInteractive},
	}
	_, err := sqlserver.NewDataSourceWithConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "password does not apply to auth.method interactive")
	assert.NotContains(t, err.Error(), standInSecret)

	cfg.Password = ""
	cfg.Auth = dsconfig.Auth{Method: sqlserver.AuthMethodServicePrincipal, TenantID: standInTenant}
	_, err = sqlserver.NewDataSourceWithConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.client_id is required for auth.method service_principal")

	// A host the engine does not know as Azure, weakened as a SQL login's may be: the token
	// would cross the wire in clear, or reach any server that answers.
	cfg.Host = "127.0.0.1"
	cfg.Auth = dsconfig.Auth{Method: sqlserver.AuthMethodInteractive}
	cfg.SSL = "disable"
	_, err = sqlserver.NewDataSourceWithConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would let an Entra ID sign-in send its access token unencrypted")

	cfg.SSL = ""
	cfg.Options = map[string]string{"trust_server_certificate": "true"}
	_, err = sqlserver.NewDataSourceWithConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "options.trust_server_certificate would hand an Entra ID sign-in's access token")
	assert.Empty(t, *requests)
}

// TestACredentialThatCannotBeBuiltFailsTheConstructor, naming the method and the database.
func TestACredentialThatCannotBeBuiltFailsTheConstructor(t *testing.T) {
	_, err := sqlserver.NewDataSourceWithConfig(dsconfig.DataSource{
		Driver: "sqlserver_gorm", Host: azureHost, Port: 1433, Database: "Example-db", LazyConnect: true,
		Auth: dsconfig.Auth{Method: sqlserver.AuthMethodServicePrincipal, TenantID: standInTenant,
			ClientID: standInClient, CertificatePath: t.TempDir() + "/missing.pem"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sqlserver: auth.method service_principal for "+standInTarget+": "+
		"cannot read auth.certificate_path")
	assert.Equal(t, 1, strings.Count(err.Error(), "auth.method"), "the method and database are named once")
	assert.Equal(t, 1, strings.Count(err.Error(), azureHost))
	assert.False(t, errors.Is(err, context.Canceled))
}
