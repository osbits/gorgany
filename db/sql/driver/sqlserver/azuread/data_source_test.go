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
	"fmt"
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
// login, and to refuse it or accept it. The login carries the token the connection signed in
// with, so the tests see which token each connection used, and how often the credential was
// asked.

// fakeTDS accepts connections on 127.0.0.1 over TLS, with a certificate made for the test: from
// the first byte, as TDS 8.0 (ssl: strict) has it, or, with inBand, negotiated inside prelogin
// packets after a prelogin in clear, as TDS 7.x (ssl: true, the default) has it. It records the
// LOGIN7 message it decrypts and every byte it read off the wire. By default it refuses the
// login with error 18456, which the engine does not retry; with accept it takes the login and
// answers every SQL batch after it as done, recording its text. A token method refuses weaker
// encryption, so the fake needs TLS to be reached at all, and the wire it records shows the
// token never crossed it in clear.
type fakeTDS struct {
	listener net.Listener
	tls      *tls.Config
	// certPath is the server's certificate as a PEM file, which options.certificate verifies
	// the connection against.
	certPath string
	// inBand, when set, negotiates TLS as TDS 7.x does, for a client with ssl: true.
	inBand bool
	// accept, when set, takes every login instead of refusing it.
	accept bool
	// idle, when set, is how long the fake waits for the login after its prelogin reply, and TLS,
	// before it aborts the connection, as the Azure SQL gateway drops one that sits idle.
	idle time.Duration
	// stall, when set, is how long the fake waits before its prelogin reply, as a slow network or
	// a busy gateway might.
	stall time.Duration
	wg    sync.WaitGroup

	mu         sync.Mutex
	dials      int
	logins     [][]byte
	sessions   [][]string
	wire       []byte
	idleClosed int
}

// startFakeTDS starts a fake that refuses every login.
func startFakeTDS(t *testing.T) *fakeTDS { return startServer(t, &fakeTDS{}) }

// startServer starts server, which carries the settings it behaves by, on a port of its own.
func startServer(t *testing.T, server *fakeTDS) *fakeTDS {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	cert, certPEM := newServerCertificate(t)
	server.listener = listener
	server.tls = &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"tds/8.0"}}
	server.certPath = writeFile(t, "server.pem", certPEM)
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
	tdsSQLBatch = 0x01
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
	deadline := time.Now().Add(30 * time.Second)
	_ = raw.SetDeadline(deadline)
	f.mu.Lock()
	f.dials++
	f.mu.Unlock()

	wire := recordingConn{Conn: raw, record: func(read []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.wire = append(f.wire, read...)
	}}
	conn, ok := f.encrypt(wire)
	if !ok {
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
		// Aborted rather than closed, so the client's next write fails as the gateway's does:
		// "write: broken pipe".
		if tcp, ok := raw.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
	}
	if err != nil || kind != tdsLogin7 {
		return
	}
	f.mu.Lock()
	f.logins = append(f.logins, login)
	f.mu.Unlock()
	if !f.accept {
		_ = writeTDS(conn, tdsReply, loginFailed())
		return
	}
	_ = raw.SetReadDeadline(deadline)
	if writeTDS(conn, tdsReply, loginAck()) != nil {
		return
	}
	f.serveBatches(conn)
}

// encrypt exchanges prelogin with the client on wire and negotiates TLS as the fake's mode has
// it, pausing for stall before its prelogin reply. It returns the encrypted connection the
// login arrives on.
func (f *fakeTDS) encrypt(wire net.Conn) (net.Conn, bool) {
	if !f.inBand {
		conn := tls.Server(wire, f.tls)
		if conn.Handshake() != nil {
			return nil, false
		}
		return conn, f.prelogin(conn)
	}

	if !f.prelogin(wire) {
		return nil, false
	}
	// At most TLS 1.2 and no session tickets, as SQL Server negotiates in prelogin packets, so
	// the handshake ends with the server's last flight and nothing follows it in that framing.
	config := f.tls.Clone()
	config.MaxVersion = tls.VersionTLS12
	config.SessionTicketsDisabled = true
	handshake := &preloginTLS{Conn: wire, handshaking: true}
	conn := tls.Server(handshake, config)
	if conn.Handshake() != nil || handshake.flush() != nil {
		return nil, false
	}
	handshake.handshaking = false
	return conn, true
}

// prelogin reads the client's prelogin from conn and answers it after stall.
func (f *fakeTDS) prelogin(conn net.Conn) bool {
	if kind, _, err := readTDS(conn); err != nil || kind != tdsPrelogin {
		return false
	}
	time.Sleep(f.stall)
	return writeTDS(conn, tdsReply, preloginReply()) == nil
}

// preloginTLS carries the server's side of a TLS handshake inside TDS prelogin packets, as TDS
// 7.x negotiates encryption (MS-TDS 3.2.5.2): while handshaking, what the client sends arrives
// in prelogin packets, and what the server writes goes back as one prelogin message when it
// next reads, as go-mssqldb's tlsHandshakeConn frames the client's side. After the handshake,
// TLS records travel bare.
type preloginTLS struct {
	net.Conn
	handshaking bool
	in, out     []byte
}

func (p *preloginTLS) Read(b []byte) (int, error) {
	if !p.handshaking {
		return p.Conn.Read(b)
	}
	if err := p.flush(); err != nil {
		return 0, err
	}
	for len(p.in) == 0 {
		kind, payload, err := readTDS(p.Conn)
		if err != nil {
			return 0, err
		}
		if kind != tdsPrelogin {
			return 0, fmt.Errorf("fakeTDS: packet type %#x inside the TLS handshake", kind)
		}
		p.in = payload
	}
	n := copy(b, p.in)
	p.in = p.in[n:]
	return n, nil
}

func (p *preloginTLS) Write(b []byte) (int, error) {
	if !p.handshaking {
		return p.Conn.Write(b)
	}
	p.out = append(p.out, b...)
	return len(b), nil
}

// flush sends what the server has written since it last read, as one prelogin message.
func (p *preloginTLS) flush() error {
	if len(p.out) == 0 {
		return nil
	}
	out := p.out
	p.out = nil
	return writeTDS(p.Conn, tdsPrelogin, out)
}

// serveBatches answers every SQL batch conn sends as done, recording its text as one session's,
// until the client closes the connection.
func (f *fakeTDS) serveBatches(conn net.Conn) {
	f.mu.Lock()
	session := len(f.sessions)
	f.sessions = append(f.sessions, nil)
	f.mu.Unlock()
	for {
		kind, batch, err := readTDS(conn)
		if err != nil || kind != tdsSQLBatch {
			return
		}
		f.mu.Lock()
		f.sessions[session] = append(f.sessions[session], batchText(batch))
		f.mu.Unlock()
		if writeTDS(conn, tdsReply, done(0)) != nil {
			return
		}
	}
}

func (f *fakeTDS) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.logins)
}

// dialCount is how many connections reached the fake.
func (f *fakeTDS) dialCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials
}

// batches returns, for each login the fake took, the text of every batch sent on it.
func (f *fakeTDS) batches() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	sessions := make([][]string, len(f.sessions))
	for i, session := range f.sessions {
		sessions[i] = append([]string(nil), session...)
	}
	return sessions
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

// batchText is the SQL of a SQL batch's payload, after its ALL_HEADERS (MS-TDS 2.2.6.7).
func batchText(batch []byte) string {
	if len(batch) < 4 {
		return ""
	}
	headers := int(binary.LittleEndian.Uint32(batch))
	if headers > len(batch) {
		return ""
	}
	text := batch[headers:]
	units := make([]uint16, len(text)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(text[2*i:])
	}
	return string(utf16.Decode(units))
}

// loginAck is a LOGINACK token for T-SQL over TDS 7.4 and the DONE token that ends the reply.
func loginAck() []byte {
	program := ucs2("fake")
	body := []byte{1} // interface: T-SQL
	body = binary.BigEndian.AppendUint32(body, 0x74000004)
	body = append(body, byte(len(program)/2))
	body = append(body, program...)
	body = append(body, 16, 0, 0x07, 0xd0) // program version

	reply := []byte{0xad}
	reply = binary.LittleEndian.AppendUint16(reply, uint16(len(body)))
	reply = append(reply, body...)
	return append(reply, done(0)...)
}

// done is a DONE token that ends a reply, with status.
func done(status uint16) []byte {
	reply := []byte{0xfd}
	reply = binary.LittleEndian.AppendUint16(reply, status)
	reply = binary.LittleEndian.AppendUint16(reply, 0)
	return binary.LittleEndian.AppendUint64(reply, 0)
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
	return append(reply, done(0x02)...) // DONE_ERROR
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
// login follows the prelogin at once, and the query runs.
func TestASlowSignInDoesNotStrandTheConnection(t *testing.T) {
	for _, ssl := range []string{"true", "strict"} {
		t.Run("ssl "+ssl, func(t *testing.T) {
			const idle = 200 * time.Millisecond
			server := startServer(t, &fakeTDS{inBand: ssl == "true", accept: true, idle: idle})
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
			cfg.SSL = ssl
			cfg.LazyConnect = true
			ds, err := sqlserver.NewDataSourceWithConfig(cfg)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, ds.Close()) })

			require.NoError(t, sqlDBOf(t, ds).PingContext(context.Background()),
				"the login reached a connection still open")
			assert.Zero(t, server.idleCloses(), "no connection sat idle through the sign-in")
			assert.Equal(t, []bool{true}, server.loginTokens("token-1"), "one connection, with the sign-in's token")
			assert.Equal(t, [][]string{{"SET XACT_ABORT ON", "select 1;"}}, server.batches(),
				"the connection ran the session's init before the query")
			getTokens, authenticates := cred.counts()
			assert.Equal(t, 1, authenticates)
			assert.Equal(t, 1, getTokens)
		})
	}
}

// TestConcurrentFirstConnectionsShareOneSignIn: queries that arrive together at a lazy
// datasource each open a connection, and the person signs in once for all of them. None of them
// dials while the sign-in is pending.
//
// The connections negotiate TLS as ssl: true does. With ssl: strict, go-mssqldb v1.11.2 writes
// the ALPN protocol into the connector's one tls.Config on every connection (getTLSConn), which
// the race detector reports once two connections open together.
func TestConcurrentFirstConnectionsShareOneSignIn(t *testing.T) {
	const n = 8
	server := startServer(t, &fakeTDS{inBand: true, accept: true})
	release := make(chan struct{})
	cred := &fakeCredential{clock: newClock(), authenticate: func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	useCredential(t, cred)

	cfg := interactiveAt(server)
	cfg.SSL = "true"
	cfg.LazyConnect = true
	ds, err := sqlserver.NewDataSourceWithConfig(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	// Each goroutine holds its connection, so the pool opens n of them rather than reusing one.
	db := sqlDBOf(t, ds)
	opened := make(chan error, n)
	for range n {
		go func() {
			conn, err := db.Conn(context.Background())
			if err == nil {
				t.Cleanup(func() { _ = conn.Close() })
			}
			opened <- err
		}()
	}
	// Whether every query has reached the sign-in by now or arrives after it, what is asserted
	// below holds; the pause only makes it likelier that they share the one in flight.
	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, server.dialCount(), "nothing dials while the person signs in")
	close(release)
	for range n {
		require.NoError(t, waitFor(t, opened, "a connection"))
	}

	getTokens, authenticates := cred.counts()
	assert.Equal(t, 1, authenticates, "one sign-in")
	assert.Equal(t, 1, getTokens, "and one token")
	assert.Equal(t, n, server.dialCount())
	assert.Equal(t, []bool{true, true, true, true, true, true, true, true}, server.loginTokens("token-1"))
	for _, session := range server.batches() {
		assert.Equal(t, []string{"SET XACT_ABORT ON"}, session, "every connection ran the session's init")
	}
}

// TestTheLoginSendsTheTokenTakenBeforeTheDial: the login does not ask the source again. The
// credential's tokens say nothing of their expiry, so the source keeps none of them and every
// request is a new one: a second request, in the middle of the handshake, would be token-2.
func TestTheLoginSendsTheTokenTakenBeforeTheDial(t *testing.T) {
	server := startServer(t, &fakeTDS{accept: true})
	cred := &fakeCredential{clock: newClock(), getToken: func(_ context.Context, n int) (azcore.AccessToken, error) {
		return azcore.AccessToken{Token: fmt.Sprintf("token-%d", n)}, nil
	}}
	useCredential(t, cred)

	cfg := interactiveAt(server)
	cfg.LazyConnect = true
	ds, err := sqlserver.NewDataSourceWithConfig(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	require.NoError(t, sqlDBOf(t, ds).PingContext(context.Background()))
	getTokens, _ := cred.counts()
	assert.Equal(t, 1, getTokens, "the source was asked once, before the dial")
	assert.Equal(t, []bool{true}, server.loginTokens("token-1"), "and the login sent that token")
}

// TestATokenThatExpiresBeforeTheLoginIsReplaced: a connection signs in with the token it took
// before it dialled, unless that token has expired by the time the login is sent, as one can
// after a handshake as slow as this. Then it asks the source again, which renews it silently:
// the person is not asked a second time.
func TestATokenThatExpiresBeforeTheLoginIsReplaced(t *testing.T) {
	const lifetime = 100 * time.Millisecond
	server := startServer(t, &fakeTDS{accept: true, stall: 4 * lifetime})
	cred := &fakeCredential{clock: newClock(), getToken: func(_ context.Context, n int) (azcore.AccessToken, error) {
		// The engine judges expiry by the time now, whatever clock the source keeps.
		expiresOn := time.Now().Add(time.Hour)
		if n == 1 {
			expiresOn = time.Now().Add(lifetime)
		}
		return azcore.AccessToken{Token: fmt.Sprintf("token-%d", n), ExpiresOn: expiresOn}, nil
	}}
	useCredential(t, cred)

	cfg := interactiveAt(server)
	cfg.LazyConnect = true
	ds, err := sqlserver.NewDataSourceWithConfig(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	require.NoError(t, sqlDBOf(t, ds).PingContext(context.Background()))
	getTokens, authenticates := cred.counts()
	assert.Equal(t, 1, authenticates, "the person signed in once")
	assert.Equal(t, 2, getTokens, "and the source renewed the token that expired during the handshake")
	assert.Equal(t, []bool{true}, server.loginTokens("token-2"), "the login carried the renewed token")
}

// TestASQLLoginSignsInAsBefore: a SQL login takes no token from anyone, and runs the session's
// init on its connection.
func TestASQLLoginSignsInAsBefore(t *testing.T) {
	server := startServer(t, &fakeTDS{inBand: true, accept: true})
	cred := &fakeCredential{clock: newClock()}
	requests := useCredential(t, cred)

	cfg := interactiveAt(server)
	cfg.SSL = "true"
	cfg.Auth = dsconfig.Auth{}
	cfg.Username, cfg.Password = "sa", standInSecret
	ds, err := sqlserver.NewDataSourceWithConfig(cfg)
	require.NoError(t, err, "the constructor connects and pings")
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	assert.Empty(t, *requests, "no credential was built")
	getTokens, authenticates := cred.counts()
	assert.Zero(t, getTokens)
	assert.Zero(t, authenticates)
	assert.Equal(t, [][]string{{"SET XACT_ABORT ON", "select 1;"}}, server.batches())
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
	assert.Zero(t, server.dialCount(), "nothing was dialled without a token")
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
