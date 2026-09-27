package azuread

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/osbits/gorgany/v2/log"
)

// PersistentCacheImportPath is the package that registers the persistent token cache
// auth.token_cache: persistent keeps a person's sign-in in. An app that sets it imports the
// package for its side effects, in pkg/provider/bootstrap.go next to this package's import.
//
// It is a package of its own because the cache links the operating system's credential store,
// the keychain on macOS through cgo, which an app that signs in once per process, or not as a
// person at all, should not have to link. It is for development machines: the production image
// is built without cgo, for scratch, and has no credential store and no person to sign in.
const PersistentCacheImportPath = "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread/persistentcache"

// persistentCacheName is the name the persistent cache is kept under: the keychain item's
// service on macOS, and the name of the encrypted file under the user's home or cache directory
// on Linux and Windows. Every datasource of every gorgany app shares it; MSAL keys what it holds
// by account, application and scope.
const persistentCacheName = "gorgany"

var (
	persistentCacheMu      sync.Mutex
	persistentCacheFactory func(name string) (azidentity.Cache, error)
)

// RegisterPersistentCache makes factory the persistent token cache of auth.token_cache:
// persistent. PersistentCacheImportPath calls it from its init with azidentity/cache's New, and
// an app has no reason to call it itself: this package takes the cache through a registration
// only so that it does not link the credential store that cache needs.
//
// It panics on a nil factory and on a second registration, since two caches answering to one
// config value is a wiring mistake with no correct resolution.
func RegisterPersistentCache(factory func(name string) (azidentity.Cache, error)) {
	if factory == nil {
		panic("azuread: RegisterPersistentCache requires a non-nil factory")
	}
	persistentCacheMu.Lock()
	defer persistentCacheMu.Unlock()
	if persistentCacheFactory != nil {
		panic("azuread: a persistent token cache is already registered")
	}
	persistentCacheFactory = factory
}

func registeredPersistentCache() func(name string) (azidentity.Cache, error) {
	persistentCacheMu.Lock()
	defer persistentCacheMu.Unlock()
	return persistentCacheFactory
}

// rememberedSignIn is how a person's sign-in outlives the process under auth.token_cache:
// persistent. The persistent cache holds its tokens, the refresh token among them, which is what
// lets the next process sign in without the person; the authentication record says whose they
// are, which the credential needs to find them.
//
// The record is account metadata, and holds no token or secret: the authority, the tenant, the
// application, the account's ID and its username. azidentity calls it non-secret. It is still
// written for its owner alone, since a username is personal data. The tokens are the credential
// store's to protect.
type rememberedSignIn struct {
	cache azidentity.Cache
	// record is the last sign-in's, or zero when the file holds none this datasource may reuse.
	// It is set when the datasource is built and never changed after.
	record azidentity.AuthenticationRecord
	path   string
	// req is the datasource's request, whose account, application and tenant a record must fit
	// (see recordFits).
	req sqlserver.AuthRequest
}

// hasRecord reports whether r holds the record of an earlier sign-in, which a nil r does not.
func (r *rememberedSignIn) hasRecord() bool {
	return r != nil && r.record != azidentity.AuthenticationRecord{}
}

// rememberedSignInFor returns how the datasource req describes remembers its sign-in, or nil
// when it does not: the memory cache, and every method that asks no person. It opens the
// persistent cache and reads the record of the last sign-in, and signs in nowhere.
//
// Its errors name the key and not the datasource, which the engine adds.
func rememberedSignInFor(req sqlserver.AuthRequest) (*rememberedSignIn, error) {
	if !asksAPerson(req.Method) || req.Auth.TokenCache != dsconfig.TokenCachePersistent {
		return nil, nil
	}
	factory := registeredPersistentCache()
	if factory == nil {
		return nil, fmt.Errorf("auth.token_cache persistent keeps the tokens in the operating system's "+
			"credential store, which registers from a package of its own so that an app that does not use "+
			"it links no keychain: add _ %q in pkg/provider/bootstrap.go next to the azuread import (it "+
			"links the OS keychain; on macOS it needs cgo)", PersistentCacheImportPath)
	}
	path, err := recordPath(req)
	if err != nil {
		return nil, err
	}
	cache, err := factory(persistentCacheName)
	if err != nil {
		return nil, fmt.Errorf("auth.token_cache persistent cannot keep the tokens in the operating system's "+
			"credential store: %w; use auth.token_cache memory to sign in once per process instead", err)
	}
	return &rememberedSignIn{cache: cache, record: readRecord(path, req), path: path, req: req}, nil
}

// userCacheDir is os.UserCacheDir, and a variable only so that tests keep their records in a
// directory of their own.
var userCacheDir = os.UserCacheDir

// recordPath is where req's authentication record is kept: auth.authentication_record_path, or
// a file under the user's cache directory named for the server, the database, the account, the
// application and the tenant, so that datasources that differ in any of them never share a
// record. The name is a hash, which says none of them.
func recordPath(req sqlserver.AuthRequest) (string, error) {
	if req.Auth.AuthenticationRecordPath != "" {
		return req.Auth.AuthenticationRecordPath, nil
	}
	dir, err := userCacheDir()
	if err != nil {
		return "", fmt.Errorf("auth.token_cache persistent has no user cache directory to keep the record of "+
			"the sign-in in (%v); set auth.authentication_record_path", err)
	}
	key := strings.ToLower(strings.Join([]string{req.Host, req.Database, req.Username, req.Auth.ClientID,
		req.Auth.TenantID}, "|"))
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, "gorgany", "azuread", hex.EncodeToString(sum[:])[:16]+".json"), nil
}

// readRecord returns the authentication record at path when req may sign in with it, and the
// zero record otherwise: no file, one azidentity cannot read as a record, a record of another
// sign-in than req describes (see recordFits), or a file another user could have written (see
// writableByOthers). A zero record makes the datasource ask the person, as it would without a
// persistent cache, and that sign-in replaces the file.
//
// The last case warns. The record decides which account in the user's own cache signs in
// silently, and a file planted in a directory others can write, such as an
// authentication_record_path in a shared one, would decide it for them. The file is checked as
// it is opened, so it cannot be swapped between the check and the read.
func readRecord(path string, req sqlserver.AuthRequest) azidentity.AuthenticationRecord {
	f, err := os.Open(path)
	if err != nil {
		return azidentity.AuthenticationRecord{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return azidentity.AuthenticationRecord{}
	}
	if why := writableByOthers(info); why != "" {
		warn(fmt.Sprintf("azuread: the record of the last sign-in for %s is ignored, so the person is asked "+
			"again: %s %s; keep it in a directory only this user can write (auth.authentication_record_path)",
			target(req), path, why))
		return azidentity.AuthenticationRecord{}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return azidentity.AuthenticationRecord{}
	}
	var record azidentity.AuthenticationRecord
	if err := json.Unmarshal(data, &record); err != nil || !recordFits(record, req) {
		return azidentity.AuthenticationRecord{}
	}
	return record
}

// recordFits reports whether record is a sign-in req may reuse (see misfit).
func recordFits(record azidentity.AuthenticationRecord, req sqlserver.AuthRequest) bool {
	return misfit(record, req) == ""
}

// misfit says why record is not a sign-in req may reuse, and is empty when it is: one of an
// account, and the account, application and tenant req names wherever it names one.
//
// The account matters most. The credential signs in silently as whoever the record names, so a
// record of another account would connect as someone the config does not say; the username is
// compared ignoring case, as Entra ID compares it. An empty username names no account, and any
// sign-in fits it, as any account may be chosen on the sign-in page. The application is compared
// only when client_id is set, and the tenant only when tenant_id is a tenant ID: a domain name, or
// organizations, cannot be compared with the ID the record holds without asking Entra ID.
func misfit(record azidentity.AuthenticationRecord, req sqlserver.AuthRequest) string {
	switch {
	case record.HomeAccountID == "" || record.Username == "":
		return "the record names no account"
	case req.Username != "" && !strings.EqualFold(record.Username, req.Username):
		return "the account that signed in is not the configured username; set username to the " +
			"account's sign-in name, or remove it"
	case req.Auth.ClientID != "" && !strings.EqualFold(record.ClientID, req.Auth.ClientID):
		return "the application signed in to is not auth.client_id"
	case isTenantID(req.Auth.TenantID) && !strings.EqualFold(record.TenantID, req.Auth.TenantID):
		return "the tenant signed in to is not auth.tenant_id"
	}
	return ""
}

// isTenantID reports whether tenant is written as a tenant ID, a GUID in its usual 8-4-4-4-12
// form, rather than as a domain name or one of Entra ID's aliases.
func isTenantID(tenant string) bool {
	if len(tenant) != 36 {
		return false
	}
	for i, c := range tenant {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case !strings.ContainsRune("0123456789abcdefABCDEF", c):
			return false
		}
	}
	return true
}

// save writes record for the next process, and warns rather than fails when it cannot: the sign-in
// it records has succeeded, and only the next start pays, by asking the person again. target
// names the database, as the token source's errors do.
//
// A record the next start would not reuse (see misfit) is not written, and warns too. username is
// only the account the sign-in page suggests, and device_code does not read it at all, so the
// person may sign in as another account, or as one whose sign-in name is not the alias configured,
// and without the warning every start would ask again with nothing saying why.
func (r *rememberedSignIn) save(record azidentity.AuthenticationRecord, target string) {
	if why := misfit(record, r.req); why != "" {
		warn(fmt.Sprintf("azuread: the sign-in for %s is not remembered, so the next start asks again: %s",
			target, why))
		return
	}
	if err := writeRecord(r.path, record); err != nil {
		warn(fmt.Sprintf("azuread: the sign-in for %s is not remembered, so the next start asks again: %v",
			target, err))
	}
}

// warn logs msg. It is a variable only so that tests can read what was logged.
var warn = func(msg string) { log.Log().Warn(msg) }

// writeRecord replaces the file at path with record, readable and writable by its owner alone. A
// directory it has to create is theirs alone too. It writes a temporary file beside path and
// renames it over the old one, so that a process starting meanwhile reads the old record or the
// new one, and never part of either.
func writeRecord(path string, record azidentity.AuthenticationRecord) (err error) {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// CreateTemp creates the file 0600.
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
