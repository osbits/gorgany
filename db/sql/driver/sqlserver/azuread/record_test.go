package azuread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auth.token_cache: persistent, with a persistent cache that opens nothing and credentials that
// sign in nowhere. What is real is the record: its path, its file, and which record a datasource
// accepts. The keychain is never touched; persistentcache's own test covers its registration.

// standInRecord is the record azidentity would return for standInUser's sign-in. Every ID in it
// is well-formed and belongs to no one.
var standInRecord = azidentity.AuthenticationRecord{
	Authority:     "https://login.microsoftonline.com",
	ClientID:      standInClient,
	HomeAccountID: "00000000-0000-0000-0000-00000000000d." + standInTenant,
	TenantID:      standInTenant,
	Username:      standInUser,
	Version:       "1.0",
}

// swapPersistentCache makes factory the registered persistent cache for the rest of t, nil
// included, and restores the registration after it.
func swapPersistentCache(t *testing.T, factory func(string) (azidentity.Cache, error)) {
	t.Helper()
	persistentCacheMu.Lock()
	saved := persistentCacheFactory
	persistentCacheFactory = factory
	persistentCacheMu.Unlock()
	t.Cleanup(func() {
		persistentCacheMu.Lock()
		persistentCacheFactory = saved
		persistentCacheMu.Unlock()
	})
}

// usePersistentCache registers a persistent cache that opens nothing, as importing
// persistentcache registers the real one, and returns the names it was opened under.
func usePersistentCache(t *testing.T) *[]string {
	t.Helper()
	var names []string
	swapPersistentCache(t, func(name string) (azidentity.Cache, error) {
		names = append(names, name)
		return azidentity.Cache{}, nil
	})
	return &names
}

// useCacheDir makes the user cache directory one of t's own, not created yet, and returns it.
func useCacheDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cache")
	saved := userCacheDir
	userCacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userCacheDir = saved })
	return dir
}

// persistent is the request of a datasource that signs in with method and keeps its tokens in
// the persistent cache.
func persistent(method string, auth dsconfig.Auth) sqlserver.AuthRequest {
	auth.TokenCache = dsconfig.TokenCachePersistent
	return request(method, azureHost, auth)
}

// nowCredential is a fake credential whose tokens are valid for an hour from now.
func nowCredential() *fakeCredential {
	return &fakeCredential{clock: &clock{now: time.Now()}}
}

// boot builds the datasource req describes as the engine builds it, over cred, and asks it for
// nothing. It returns the token source, what the datasource remembers its sign-in with, and its
// Close.
func boot(t *testing.T, req sqlserver.AuthRequest, cred *fakeCredential) (*cachingTokenSource, *rememberedSignIn, context.CancelFunc) {
	t.Helper()
	root, closeDatasource := context.WithCancel(context.Background())
	t.Cleanup(closeDatasource)
	req.Context = root

	var remembered *rememberedSignIn
	saved := credentialFor
	credentialFor = func(_ sqlserver.AuthRequest, r *rememberedSignIn) (azcore.TokenCredential, error) {
		remembered = r
		return cred, nil
	}
	ts, err := authenticate(req)
	credentialFor = saved
	require.NoError(t, err)
	return ts.(*cachingTokenSource), remembered, closeDatasource
}

// start is one process starting: the datasource req describes is built as the engine builds it,
// over cred, and asked for its first token. It returns what the datasource remembers its sign-in
// with.
func start(t *testing.T, req sqlserver.AuthRequest, cred *fakeCredential) *rememberedSignIn {
	t.Helper()
	s, remembered, _ := boot(t, req, cred)
	_, err := s.Token(context.Background())
	require.NoError(t, err)
	return remembered
}

// captureWarnings collects what the package warns for the rest of t.
func captureWarnings(t *testing.T) *[]string {
	t.Helper()
	var warnings []string
	saved := warn
	warn = func(msg string) { warnings = append(warnings, msg) }
	t.Cleanup(func() { warn = saved })
	return &warnings
}

func writeRecordFile(t *testing.T, path string, record azidentity.AuthenticationRecord) {
	t.Helper()
	data, err := json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func readRecordFile(t *testing.T, path string) azidentity.AuthenticationRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var record azidentity.AuthenticationRecord
	require.NoError(t, json.Unmarshal(data, &record))
	return record
}

func TestRegisterPersistentCacheRejectsNilAndASecond(t *testing.T) {
	swapPersistentCache(t, nil)

	assert.PanicsWithValue(t, "azuread: RegisterPersistentCache requires a non-nil factory", func() {
		RegisterPersistentCache(nil)
	})
	fake := func(string) (azidentity.Cache, error) { return azidentity.Cache{}, nil }
	RegisterPersistentCache(fake)
	assert.PanicsWithValue(t, "azuread: a persistent token cache is already registered", func() {
		RegisterPersistentCache(fake)
	})
}

// TestPersistentCacheWithoutItsImportNamesThePackage: the cache links the keychain, so it comes
// from a package of its own, and the boot error quotes the import line. The memory cache, which
// an unset token_cache means, is today's behaviour and needs nothing.
func TestPersistentCacheWithoutItsImportNamesThePackage(t *testing.T) {
	swapPersistentCache(t, nil)
	useCacheDir(t)

	for _, method := range []string{sqlserver.AuthMethodInteractive, sqlserver.AuthMethodDeviceCode} {
		_, err := authenticate(persistent(method, dsconfig.Auth{}))
		require.Errorf(t, err, "%s", method)
		assert.Contains(t, err.Error(), `add _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread/persistentcache" `+
			"in pkg/provider/bootstrap.go next to the azuread import")
		assert.Contains(t, err.Error(), "it links the OS keychain; on macOS it needs cgo")
	}

	_, err := sqlserver.NewDataSourceWithConfig(dsconfig.DataSource{
		Driver: "sqlserver_gorm", Host: azureHost, Port: 1433, Database: "Example-db", LazyConnect: true,
		Auth: dsconfig.Auth{Method: sqlserver.AuthMethodInteractive, TokenCache: dsconfig.TokenCachePersistent},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sqlserver: auth.method interactive for "+standInTarget+": auth.token_cache persistent")
	assert.Contains(t, err.Error(), PersistentCacheImportPath)

	for _, cache := range []string{"", dsconfig.TokenCacheMemory} {
		req := request(sqlserver.AuthMethodInteractive, azureHost, dsconfig.Auth{TokenCache: cache})
		remembered, err := rememberedSignInFor(req)
		require.NoErrorf(t, err, "token_cache %q", cache)
		assert.Nilf(t, remembered, "token_cache %q remembers nothing", cache)
	}
}

// TestAPersistentCacheThatCannotOpenFailsTheBoot with the reason, and the way back to one
// sign-in per process.
func TestAPersistentCacheThatCannotOpenFailsTheBoot(t *testing.T) {
	swapPersistentCache(t, func(string) (azidentity.Cache, error) {
		return azidentity.Cache{}, errors.New(`persistent storage isn't available due to error "stand-in"`)
	})

	_, err := authenticate(persistent(sqlserver.AuthMethodInteractive,
		dsconfig.Auth{AuthenticationRecordPath: filepath.Join(t.TempDir(), "record.json")}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.token_cache persistent cannot keep the tokens in the operating system's "+
		`credential store: persistent storage isn't available due to error "stand-in"`)
	assert.Contains(t, err.Error(), "use auth.token_cache memory to sign in once per process instead")
}

// TestAuthenticationRecordIsWrittenPrivately: the first start asks the person, and writes the
// record of that sign-in under the user's cache directory, for its owner alone, and no token
// with it.
func TestAuthenticationRecordIsWrittenPrivately(t *testing.T) {
	names := usePersistentCache(t)
	dir := useCacheDir(t)

	cred := nowCredential()
	remembered := start(t, persistent(sqlserver.AuthMethodInteractive, dsconfig.Auth{}), cred)
	_, authenticates := cred.counts()
	assert.Equal(t, 1, authenticates, "no record yet, so the person was asked")
	assert.Equal(t, []string{persistentCacheName}, *names)
	require.NotNil(t, remembered)
	assert.False(t, remembered.hasRecord())

	recordDir := filepath.Join(dir, "gorgany", "azuread")
	assert.Equal(t, recordDir, filepath.Dir(remembered.path))
	assert.Regexp(t, `^[0-9a-f]{16}\.json$`, filepath.Base(remembered.path))
	assert.Equal(t, standInRecord, readRecordFile(t, remembered.path))
	data, err := os.ReadFile(remembered.path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "token-1", "the record is account metadata, and holds no token")

	entries, err := os.ReadDir(recordDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary file is left behind")

	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix mode bits; the file inherits the user profile's ACL")
	}
	info, err := os.Stat(remembered.path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the record is its owner's alone")
	for _, created := range []string{dir, filepath.Join(dir, "gorgany"), recordDir} {
		info, err := os.Stat(created)
		require.NoError(t, err)
		assert.Equalf(t, os.FileMode(0o700), info.Mode().Perm(), "%s is its owner's alone", created)
	}
}

// TestTheRecordIsReplacedNotRewritten: a new record is written beside the old one and renamed over
// it, so a process starting meanwhile reads one whole record or the other. A hard link to the old
// file shows it: rewritten in place, the link would read the new record too.
func TestTheRecordIsReplacedNotRewritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"stale":true}`), 0o644))
	earlier := filepath.Join(dir, "earlier.json")
	if err := os.Link(path, earlier); err != nil {
		t.Skipf("no hard links here: %v", err)
	}

	require.NoError(t, writeRecord(path, standInRecord))

	old, err := os.ReadFile(earlier)
	require.NoError(t, err)
	assert.Equal(t, `{"stale":true}`, string(old), "the old file was left whole, and replaced")
	assert.Equal(t, standInRecord, readRecordFile(t, path))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "the record and the link, and no temporary file")
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the new file is private, whatever the old one was")
	}
}

// TestASecondStartWithARecordAsksNoOne: the next process finds the record, builds its credential
// from it and the persistent cache, and gets its token without Authenticate. The credential still
// never prompts from GetToken.
func TestASecondStartWithARecordAsksNoOne(t *testing.T) {
	names := usePersistentCache(t)
	useCacheDir(t)
	req := persistent(sqlserver.AuthMethodInteractive, dsconfig.Auth{TenantID: standInTenant, ClientID: standInClient})

	first := nowCredential()
	start(t, req, first)
	_, authenticates := first.counts()
	require.Equal(t, 1, authenticates)

	second := nowCredential()
	remembered := start(t, req, second)
	getTokens, authenticates := second.counts()
	assert.Zero(t, authenticates, "the second start asked no one")
	assert.Equal(t, 1, getTokens, "it signed in from the cache")
	assert.Equal(t, standInRecord, remembered.record)
	assert.Equal(t, []string{persistentCacheName, persistentCacheName}, *names)

	client, err := clientOptions(req)
	require.NoError(t, err)
	interactive := interactiveOptions(req, client, remembered)
	assert.Equal(t, standInRecord, interactive.AuthenticationRecord)
	assert.Equal(t, remembered.cache, interactive.Cache)
	assert.True(t, interactive.DisableAutomaticAuthentication, "a GetToken must never open a browser")

	device := deviceCodeOptions(req, client, remembered)
	assert.Equal(t, standInRecord, device.AuthenticationRecord)
	assert.Equal(t, remembered.cache, device.Cache)
	assert.True(t, device.DisableAutomaticAuthentication)
}

// TestARecordTheCacheCannotAnswerAsksThePersonOnce: the refresh token expired, or the keychain
// lost it. The start asks the person, once, as without a persistent cache, and writes the new
// record.
func TestARecordTheCacheCannotAnswerAsksThePersonOnce(t *testing.T) {
	usePersistentCache(t)
	path := filepath.Join(t.TempDir(), "record.json")
	stale := standInRecord
	stale.HomeAccountID = "00000000-0000-0000-0000-00000000000e." + standInTenant
	writeRecordFile(t, path, stale)

	cred := nowCredential()
	cred.getToken = func(_ context.Context, n int) (azcore.AccessToken, error) {
		if n == 1 {
			return azcore.AccessToken{}, errors.New("stand-in: the cache holds no token for the account")
		}
		return azcore.AccessToken{Token: "token-2", ExpiresOn: time.Now().Add(time.Hour)}, nil
	}
	start(t, persistent(sqlserver.AuthMethodInteractive, dsconfig.Auth{AuthenticationRecordPath: path}), cred)

	getTokens, authenticates := cred.counts()
	assert.Equal(t, 1, authenticates, "one prompt, after the silent attempt failed")
	assert.Equal(t, 2, getTokens, "the silent attempt, and the token after the sign-in")
	assert.Equal(t, standInRecord, readRecordFile(t, path), "the new sign-in replaced the record")
}

// TestARecordForAnotherUserIsIgnored: signing in silently from it would connect as someone the
// config does not name. The person is asked, and their record replaces it.
func TestARecordForAnotherUserIsIgnored(t *testing.T) {
	usePersistentCache(t)
	path := filepath.Join(t.TempDir(), "record.json")
	other := standInRecord
	other.Username = "someone.else@example.com"
	writeRecordFile(t, path, other)

	cred := nowCredential()
	remembered := start(t, persistent(sqlserver.AuthMethodInteractive, dsconfig.Auth{AuthenticationRecordPath: path}), cred)
	assert.False(t, remembered.hasRecord(), "the record names another account")
	getTokens, authenticates := cred.counts()
	assert.Equal(t, 1, authenticates, "so the person was asked")
	assert.Equal(t, 1, getTokens, "and nothing was tried silently")
	assert.Equal(t, standInRecord, readRecordFile(t, path))
}

// TestASignInAsAnotherAccountIsNotRemembered: username is only the account the sign-in page
// suggests, and device_code does not read it, so the person may sign in as someone else, or as the
// account under a sign-in name other than the alias configured. The next start would ignore that
// record and ask again, so it is not written, and a warning says why rather than every start
// asking with nothing said.
func TestASignInAsAnotherAccountIsNotRemembered(t *testing.T) {
	for _, method := range []string{sqlserver.AuthMethodInteractive, sqlserver.AuthMethodDeviceCode} {
		t.Run(method, func(t *testing.T) {
			usePersistentCache(t)
			warnings := captureWarnings(t)
			path := filepath.Join(t.TempDir(), "record.json")
			req := persistent(method, dsconfig.Auth{AuthenticationRecordPath: path})

			cred := nowCredential()
			cred.record = standInRecord
			cred.record.Username = "someone.else@example.com"
			start(t, req, cred)

			require.Len(t, *warnings, 1)
			assert.Equal(t, "azuread: the sign-in for "+standInTarget+" is not remembered, so the next start asks "+
				"again: the account that signed in is not the configured username; set username to the account's "+
				"sign-in name, or remove it", (*warnings)[0])
			assert.NotContains(t, (*warnings)[0], "someone.else@example.com", "the warning names no account")
			_, err := os.Stat(path)
			assert.Truef(t, os.IsNotExist(err), "no record the next start would ignore is written: %v", err)

			next := nowCredential()
			start(t, req, next)
			_, authenticates := next.counts()
			assert.Equal(t, 1, authenticates, "so the next start asks again, as the warning said")
			assert.Len(t, *warnings, 1, "and signed in as the configured account, it is remembered")
			assert.Equal(t, standInRecord, readRecordFile(t, path))
		})
	}
}

// TestAResumeCutShortAsksNoOne: the silent sign-in from the persistent cache is ended by a Close or
// by its own bound, and nobody is asked. After the silent request has found the authority, MSAL
// opens the browser before it looks at the context again, so a prompt then would ask the person
// for a sign-in the datasource has already given up. The error says the resume failed, not that
// the person did not finish.
func TestAResumeCutShortAsksNoOne(t *testing.T) {
	// hanging answers every silent GetToken as azidentity does once one is cut short: with an
	// AuthenticationRequiredError, when its context ends. After a prompt, which the source must
	// not open, it answers at once, so a source that does open one fails the test rather than
	// hanging it.
	hanging := func(entered chan<- struct{}) *fakeCredential {
		cred := nowCredential()
		cred.getToken = func(ctx context.Context, n int) (azcore.AccessToken, error) {
			if _, authenticates := cred.counts(); authenticates > 0 {
				return azcore.AccessToken{Token: fmt.Sprintf("token-%d", n), ExpiresOn: time.Now().Add(time.Hour)}, nil
			}
			if entered != nil {
				entered <- struct{}{}
			}
			<-ctx.Done()
			return azcore.AccessToken{}, &azidentity.AuthenticationRequiredError{}
		}
		return cred
	}
	withRecord := func(t *testing.T) sqlserver.AuthRequest {
		usePersistentCache(t)
		path := filepath.Join(t.TempDir(), "record.json")
		writeRecordFile(t, path, standInRecord)
		return persistent(sqlserver.AuthMethodInteractive, dsconfig.Auth{AuthenticationRecordPath: path})
	}

	t.Run("a Close", func(t *testing.T) {
		entered := make(chan struct{}, 1)
		cred := hanging(entered)
		s, remembered, closeDatasource := boot(t, withRecord(t), cred)
		require.True(t, remembered.hasRecord())

		result := make(chan error, 1)
		go func() {
			_, err := s.Token(context.Background())
			result <- err
		}()
		waitFor(t, entered, "the resume")
		closeDatasource()
		err := waitFor(t, result, "the abandoned resume's return")
		require.Error(t, err)
		assert.Equal(t, "azuread: resuming the interactive sign-in for "+standInTarget+" from the persistent "+
			"token cache was abandoned, and nobody was asked: the datasource was closed: context canceled", err.Error())
		require.ErrorIs(t, err, context.Canceled)
		_, authenticates := cred.counts()
		assert.Zero(t, authenticates, "no prompt for a datasource already closed")
	})

	t.Run("login_timeout", func(t *testing.T) {
		cred := hanging(nil)
		req := withRecord(t)
		req.LoginTimeout = 50 * time.Millisecond
		s, _, _ := boot(t, req, cred)

		_, err := s.Token(context.Background())
		require.Error(t, err)
		assert.Equal(t, "azuread: the interactive sign-in for "+standInTarget+" could not be resumed from the "+
			"persistent token cache within 50ms, so nobody was asked; Entra ID may be unreachable, and the "+
			"next connection tries again; raise auth.login_timeout if it needs longer: context deadline exceeded",
			err.Error())
		require.ErrorIs(t, err, context.DeadlineExceeded)
		getTokens, authenticates := cred.counts()
		assert.Zero(t, authenticates, "no prompt with no time left to finish it")
		assert.Equal(t, 1, getTokens)

		_, err = s.Token(context.Background())
		require.Error(t, err, "nobody signed in, so the next connection resumes again")
		getTokens, authenticates = cred.counts()
		assert.Equal(t, 2, getTokens)
		assert.Zero(t, authenticates)
	})

	t.Run("its own bound", func(t *testing.T) {
		cred := hanging(nil)
		s, _, _ := boot(t, withRecord(t), cred)
		s.renewTimeout = 30 * time.Millisecond

		start := time.Now()
		_, err := s.Token(context.Background())
		require.Error(t, err)
		assert.Less(t, time.Since(start), 5*time.Second, "the resume is not given the sign-in's five minutes")
		assert.Equal(t, "azuread: the interactive sign-in for "+standInTarget+" could not be resumed from the "+
			"persistent token cache within 30ms, so nobody was asked; Entra ID may be unreachable, and the "+
			"next connection tries again: context deadline exceeded", err.Error())
		_, authenticates := cred.counts()
		assert.Zero(t, authenticates)
	})
}

// TestAResumeLeavesThePersonTheSignInsTime: the resume is bounded like a renewal, so a slow cache
// cannot use up the time the person has to finish the prompt that follows when it cannot answer.
func TestAResumeLeavesThePersonTheSignInsTime(t *testing.T) {
	usePersistentCache(t)
	path := filepath.Join(t.TempDir(), "record.json")
	writeRecordFile(t, path, standInRecord)
	req := persistent(sqlserver.AuthMethodInteractive, dsconfig.Auth{AuthenticationRecordPath: path})
	req.LoginTimeout = 10 * time.Minute

	var resumeLeft, promptLeft time.Duration
	cred := nowCredential()
	cred.getToken = func(ctx context.Context, n int) (azcore.AccessToken, error) {
		if n == 1 {
			deadline, _ := ctx.Deadline()
			resumeLeft = time.Until(deadline)
			return azcore.AccessToken{}, &azidentity.AuthenticationRequiredError{}
		}
		return azcore.AccessToken{Token: "token-2", ExpiresOn: time.Now().Add(time.Hour)}, nil
	}
	cred.authenticate = func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		promptLeft = time.Until(deadline)
		return nil
	}
	s, _, _ := boot(t, req, cred)
	_, err := s.Token(context.Background())
	require.NoError(t, err)

	_, authenticates := cred.counts()
	assert.Equal(t, 1, authenticates, "the cache could not answer, so the person was asked")
	assert.LessOrEqual(t, resumeLeft, sqlserver.DefaultLoginTimeout, "the resume had a renewal's minute")
	assert.Greater(t, resumeLeft, sqlserver.DefaultLoginTimeout-10*time.Second)
	assert.Greater(t, promptLeft, 10*time.Minute-10*time.Second, "and the person the sign-in's ten")
}

// TestWhichRecordsFit: the account, application and tenant must be the ones the config names
// wherever it names one.
func TestWhichRecordsFit(t *testing.T) {
	with := func(change func(*azidentity.AuthenticationRecord)) azidentity.AuthenticationRecord {
		record := standInRecord
		change(&record)
		return record
	}
	named := request(sqlserver.AuthMethodInteractive, azureHost, dsconfig.Auth{TenantID: standInTenant, ClientID: standInClient})
	unnamed := request(sqlserver.AuthMethodInteractive, azureHost, dsconfig.Auth{})
	unnamed.Username = ""
	byDomain := request(sqlserver.AuthMethodDeviceCode, azureHost, dsconfig.Auth{TenantID: "example.onmicrosoft.com"})

	for name, tc := range map[string]struct {
		record azidentity.AuthenticationRecord
		req    sqlserver.AuthRequest
		fits   bool
	}{
		"the same sign-in":                    {standInRecord, named, true},
		"the username in another case":        {with(func(r *azidentity.AuthenticationRecord) { r.Username = strings.ToUpper(standInUser) }), named, true},
		"another account":                     {with(func(r *azidentity.AuthenticationRecord) { r.Username = "someone.else@example.com" }), named, false},
		"any account, when none is named":     {with(func(r *azidentity.AuthenticationRecord) { r.Username = "someone.else@example.com" }), unnamed, true},
		"another application":                 {with(func(r *azidentity.AuthenticationRecord) { r.ClientID = standInObject }), named, false},
		"the application in another case":     {with(func(r *azidentity.AuthenticationRecord) { r.ClientID = strings.ToUpper(standInClient) }), named, true},
		"another tenant":                      {with(func(r *azidentity.AuthenticationRecord) { r.TenantID = standInObject }), named, false},
		"a tenant named by domain":            {standInRecord, byDomain, true},
		"any application, when none is named": {with(func(r *azidentity.AuthenticationRecord) { r.ClientID = standInObject }), unnamed, true},
		"no account ID":                       {with(func(r *azidentity.AuthenticationRecord) { r.HomeAccountID = "" }), named, false},
		"no username":                         {with(func(r *azidentity.AuthenticationRecord) { r.Username = "" }), unnamed, false},
	} {
		assert.Equalf(t, tc.fits, recordFits(tc.record, tc.req), "%s", name)
	}

	dir := t.TempDir()
	for name, content := range map[string]string{
		"not JSON":             "not a record",
		"an unknown version":   `{"homeAccountId":"x","username":"` + standInUser + `","version":"2.0"}`,
		"no version":           `{"homeAccountId":"x","username":"` + standInUser + `"}`,
		"another kind of JSON": `["` + standInUser + `"]`,
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		assert.Zerof(t, readRecord(path, named), "%s is no record", name)
	}
	assert.Zero(t, readRecord(filepath.Join(dir, "missing.json"), named))
}

// TestTheRecordPathNamesNoOne: the default file is named by a hash, which says nothing of the
// server, the database or the account, and differs whenever any of them does.
func TestTheRecordPathNamesNoOne(t *testing.T) {
	dir := useCacheDir(t)
	req := persistent(sqlserver.AuthMethodInteractive, dsconfig.Auth{TenantID: standInTenant, ClientID: standInClient})

	path, err := recordPath(req)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(path, filepath.Join(dir, "gorgany", "azuread")+string(filepath.Separator)))
	for _, value := range []string{standInUser, "example", "Example-db", standInTenant, standInClient} {
		assert.NotContains(t, filepath.Base(path), value)
	}

	sameAccount := req
	sameAccount.Username = strings.ToUpper(req.Username)
	samePath, err := recordPath(sameAccount)
	require.NoError(t, err)
	assert.Equal(t, path, samePath, "an account's name is not case-sensitive")

	for name, change := range map[string]func(*sqlserver.AuthRequest){
		"another account":     func(r *sqlserver.AuthRequest) { r.Username = "someone.else@example.com" },
		"another database":    func(r *sqlserver.AuthRequest) { r.Database = "Other-db" },
		"another server":      func(r *sqlserver.AuthRequest) { r.Host = usGovHost },
		"another application": func(r *sqlserver.AuthRequest) { r.Auth.ClientID = standInObject },
		"another tenant":      func(r *sqlserver.AuthRequest) { r.Auth.TenantID = standInObject },
	} {
		other := req
		change(&other)
		otherPath, err := recordPath(other)
		require.NoError(t, err)
		assert.NotEqualf(t, path, otherPath, "%s has a record of its own", name)
	}

	req.Auth.AuthenticationRecordPath = "/var/lib/stand-in/record.json"
	path, err = recordPath(req)
	require.NoError(t, err)
	assert.Equal(t, "/var/lib/stand-in/record.json", path, "a configured path is used as it is")

	userCacheDir = func() (string, error) { return "", errors.New("neither $XDG_CACHE_HOME nor $HOME is defined") }
	_, err = recordPath(persistent(sqlserver.AuthMethodInteractive, dsconfig.Auth{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither $XDG_CACHE_HOME nor $HOME is defined")
	assert.Contains(t, err.Error(), "set auth.authentication_record_path")
}

// TestARecordThatCannotBeWrittenWarns rather than failing a sign-in that worked; only the next
// start pays, by asking again.
func TestARecordThatCannotBeWrittenWarns(t *testing.T) {
	usePersistentCache(t)
	notADirectory := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notADirectory, nil, 0o600))
	warnings := captureWarnings(t)

	cred := nowCredential()
	start(t, persistent(sqlserver.AuthMethodInteractive,
		dsconfig.Auth{AuthenticationRecordPath: filepath.Join(notADirectory, "record.json")}), cred)

	require.Len(t, *warnings, 1)
	assert.Contains(t, (*warnings)[0], "azuread: the sign-in for "+standInTarget+" is not remembered, so the next start asks again")
	assert.NotContains(t, (*warnings)[0], "token-1")
}

// TestMethodsThatAskNoOneRememberNothing: the engine refuses token_cache for them, and a request
// built without it is not read for one either.
func TestMethodsThatAskNoOneRememberNothing(t *testing.T) {
	swapPersistentCache(t, nil)
	for _, method := range []string{sqlserver.AuthMethodAzureCLI, sqlserver.AuthMethodAzureDefault,
		sqlserver.AuthMethodManagedIdentity, sqlserver.AuthMethodWorkloadIdentity, sqlserver.AuthMethodServicePrincipal} {
		remembered, err := rememberedSignInFor(persistent(method, dsconfig.Auth{}))
		require.NoErrorf(t, err, "%s", method)
		assert.Nilf(t, remembered, "%s", method)
	}
}
