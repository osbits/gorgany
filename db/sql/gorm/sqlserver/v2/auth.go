package v2

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
)

// The sign-in methods databases.<name>.auth.method selects. "sql", or no auth block at all,
// signs in with the top-level username and password. The others sign in with a Microsoft
// Entra ID token through an Authenticator registered under their name. They link the Azure
// identity SDK, MSAL and a browser opener, which an app signing in with a SQL login should not
// have to link, so they register from a package of their own, AzureADImportPath, which the app
// imports when it wants them. An app may also register an Authenticator of its own under one of
// these names (see RegisterAuthenticator).
const (
	AuthMethodSQL              = "sql"
	AuthMethodInteractive      = "interactive"
	AuthMethodDeviceCode       = "device_code"
	AuthMethodAzureCLI         = "azure_cli"
	AuthMethodAzureDefault     = "azure_default"
	AuthMethodServicePrincipal = "service_principal"
)

// AzureADImportPath is the package that registers the Entra ID methods above. An app that
// signs in with one imports it for its side effects, in pkg/provider/bootstrap.go next to its
// driver import; it registers the sqlserver_gorm driver too.
const AzureADImportPath = "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread"

// The bounds of one sign-in when auth.login_timeout is unset.
//
// A person signing in, in a browser or with a device code, may have to find the window, pick
// an account and answer MFA, so interactive and device_code get five minutes. Every other
// method signs in without anyone: the Azure CLI answers from its own cache, and a managed
// identity or a service principal from one request, so a minute is already generous, and a
// longer wait would only delay the error of a tool that hangs.
const (
	DefaultLoginTimeout            = time.Minute
	DefaultInteractiveLoginTimeout = 5 * time.Minute
)

// loginTimeoutFor returns how long one sign-in with method may take: configured, which is
// auth.login_timeout, or the method's default.
func loginTimeoutFor(method string, configured time.Duration) time.Duration {
	switch {
	case configured > 0:
		return configured
	case method == AuthMethodInteractive || method == AuthMethodDeviceCode:
		return DefaultInteractiveLoginTimeout
	default:
		return DefaultLoginTimeout
	}
}

// TokenSource hands out the access token a connection signs in with.
//
// go-mssqldb calls Token for every physical connection it opens, with the context of the
// dial, so a pool of ten asks ten times: an implementation caches its token and shares one
// acquisition between callers that arrive together. It must return promptly once ctx is
// done. Its errors must never contain the token or a secret.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// AuthRequest is what an Authenticator is given to build the TokenSource of one datasource.
type AuthRequest struct {
	// Context is done once the datasource is closed. An acquisition that outlives the dial
	// that asked for it — an interactive sign-in still waiting on the browser, say — runs
	// under it, so Close abandons it instead of leaving it to its timeout. It is the
	// datasource's lifetime, which is why it is a field rather than an argument.
	Context context.Context

	// Method is auth.method, canonical: lowercase and trimmed.
	Method string
	// Auth is the whole auth block. Which of its fields the method takes has been checked
	// already for the built-in methods.
	Auth dsconfig.Auth
	// Username is the top-level username: the account an interactive sign-in suggests, and
	// nothing to the other built-in methods.
	Username string
	// Host and Database name the server and database the token is for.
	Host     string
	Database string
	// Target names them as the engine's errors do: host:port/db, or host\instance/db. An
	// Authenticator whose errors name the database uses it, so that a log line names it one
	// way whichever layer wrote it.
	Target string
	// Scope is the token scope: auth.scope when set, otherwise derived from the host (see
	// ResolveScope).
	Scope string
	// Cloud is the Azure cloud whose authority signs in: the host's, or the one auth.scope's
	// Azure SQL audience names for a host that is not an Azure SQL endpoint (see ResolveCloud).
	Cloud string
	// LoginTimeout bounds one sign-in: auth.login_timeout, or DefaultInteractiveLoginTimeout
	// for interactive and device_code, and DefaultLoginTimeout for every other method.
	LoginTimeout time.Duration
}

// Authenticator builds the TokenSource for one datasource. It is called once, when the
// datasource is constructed, and must not sign in there: the datasource asks for the first
// token when it warms up, or on its first connection with lazy_connect.
//
// The engine wraps an error it returns with the method and the database, so the error says
// only what is wrong. The TokenSource's errors are not wrapped that way on every path, since
// go-mssqldb reports them to whichever query opened the connection, so they name the database
// themselves, as AuthRequest.Target does.
type Authenticator func(AuthRequest) (TokenSource, error)

var (
	authMu         sync.RWMutex
	authenticators = map[string]Authenticator{}
)

// RegisterAuthenticator makes auth.method name sign in through a.
//
// It panics on an empty or non-canonical name, on a nil Authenticator, on "sql", which is the
// SQL login and not a token method, and on a name registered already: two authenticators
// answering to one config value is a wiring mistake with no correct resolution. The name must
// be lowercase and trimmed, because that is how the config parser hands auth.method over.
//
// A built-in method's name keeps the built-in rules for which auth keys it takes. Any other
// name is the registrant's to define, and its auth block reaches the Authenticator unchecked.
func RegisterAuthenticator(name string, a Authenticator) {
	if name == "" {
		panic("sqlserver: RegisterAuthenticator requires a non-empty method name")
	}
	if name != strings.ToLower(strings.TrimSpace(name)) {
		panic(fmt.Sprintf("sqlserver: RegisterAuthenticator(%q): the name must be lowercase and trimmed, "+
			"as auth.method arrives from the config", name))
	}
	if name == AuthMethodSQL {
		panic(`sqlserver: RegisterAuthenticator("sql"): sql is the SQL login, which needs no authenticator`)
	}
	if a == nil {
		panic(fmt.Sprintf("sqlserver: RegisterAuthenticator(%q) requires a non-nil Authenticator", name))
	}

	authMu.Lock()
	defer authMu.Unlock()
	if _, exists := authenticators[name]; exists {
		panic(fmt.Sprintf("sqlserver: auth method %q is already registered", name))
	}
	authenticators[name] = a
}

// RegisteredAuthMethods returns the names of the registered authenticators, sorted. The SQL
// login is always available and is not among them.
func RegisteredAuthMethods() []string {
	authMu.RLock()
	defer authMu.RUnlock()

	names := make([]string, 0, len(authenticators))
	for name := range authenticators {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func lookupAuthenticator(name string) (Authenticator, bool) {
	authMu.RLock()
	defer authMu.RUnlock()
	a, ok := authenticators[name]
	return a, ok
}

// resolvedAuth is how a datasource signs in: the canonical method, and the Authenticator for
// a token method, nil for the SQL login.
type resolvedAuth struct {
	method        string
	authenticator Authenticator
}

func (r resolvedAuth) sqlLogin() bool { return r.authenticator == nil }

// authMethodOf returns cfg's auth.method as it is matched: lowercase, trimmed, and "sql" when
// unset.
func authMethodOf(cfg dsconfig.DataSource) string {
	method := strings.ToLower(strings.TrimSpace(cfg.Auth.Method))
	if method == "" {
		return AuthMethodSQL
	}
	return method
}

// resolveAuth decides how cfg signs in, and refuses an auth block the method cannot honour.
//
// A built-in token method's field rules are checked before whether it is registered at all,
// so a config that is wrong is told so before it is told the method has no authenticator.
// Nothing here echoes a value from the auth block, which may hold a secret.
func resolveAuth(cfg dsconfig.DataSource) (resolvedAuth, error) {
	method := authMethodOf(cfg)
	if method == AuthMethodSQL {
		return resolvedAuth{method: method}, checkSQLLogin(cfg)
	}

	if rules, builtin := tokenMethodRules[method]; builtin {
		if err := rules.check(method, cfg); err != nil {
			return resolvedAuth{}, err
		}
		a, ok := lookupAuthenticator(method)
		if !ok {
			return resolvedAuth{}, fmt.Errorf("sqlserver: auth.method %q signs in with Microsoft Entra ID, "+
				"whose methods register from a package of their own so that an app on a SQL login links "+
				"no Azure SDK: add _ %q in pkg/provider/bootstrap.go next to your driver import (it "+
				"registers the sqlserver_gorm driver too)", method, AzureADImportPath)
		}
		return resolvedAuth{method: method, authenticator: a}, nil
	}

	if a, ok := lookupAuthenticator(method); ok {
		return resolvedAuth{method: method, authenticator: a}, nil
	}
	if pending, ok := pendingMethods[method]; ok {
		return resolvedAuth{}, errors.New(pending)
	}
	return resolvedAuth{}, unknownMethodError(method)
}

// checkSQLLogin checks that the SQL login has what it signs in with, and nothing it would
// ignore.
func checkSQLLogin(cfg dsconfig.DataSource) error {
	if set := setAuthKeys(cfg.Auth); len(set) > 0 {
		return fmt.Errorf("sqlserver: auth.%s does not apply to auth.method sql, which signs in with the "+
			"top-level username and password; remove it, or choose the method it belongs to", set[0])
	}
	const unset = "; an unset ${VAR} placeholder in the config reads as empty"
	switch {
	case cfg.Username == "" && cfg.Password == "":
		return errors.New("sqlserver: no credentials: auth.method sql signs in with username and password, " +
			"and both are empty" + unset)
	case cfg.Username == "":
		return errors.New("sqlserver: username is required to sign in with auth.method sql" + unset)
	case cfg.Password == "":
		return errors.New("sqlserver: password is required to sign in with auth.method sql" + unset)
	}
	return nil
}

// setAuthKeys names the auth keys cfg sets besides method, in a fixed order, as the config
// spells them.
func setAuthKeys(a dsconfig.Auth) []string {
	var set []string
	for _, field := range authFields(a) {
		if field.set {
			set = append(set, field.key)
		}
	}
	return set
}

type authField struct {
	key string
	set bool
}

func authFields(a dsconfig.Auth) []authField {
	return []authField{
		{"tenant_id", a.TenantID != ""},
		{"client_id", a.ClientID != ""},
		{"client_secret", a.ClientSecret != ""},
		{"certificate_path", a.CertificatePath != ""},
		{"certificate_password", a.CertificatePassword != ""},
		{"send_certificate_chain", a.SendCertificateChain},
		{"resource_id", a.ResourceID != ""},
		{"object_id", a.ObjectID != ""},
		{"token_file_path", a.TokenFilePath != ""},
		{"redirect_url", a.RedirectURL != ""},
		{"scope", a.Scope != ""},
		{"login_timeout", a.LoginTimeout != 0},
	}
}

// methodRules are the auth keys one built-in token method takes. scope and login_timeout
// apply to every token method, and are not listed.
type methodRules struct {
	allowed map[string]bool
	// hints explain the refusal of a key the reader is likely to have meant for something.
	hints map[string]string
	// extra adds the rules a set of allowed keys does not express.
	extra func(a dsconfig.Auth) error
}

var tokenMethodRules = map[string]methodRules{
	AuthMethodInteractive: {
		allowed: keySet("tenant_id", "client_id", "redirect_url"),
		hints:   map[string]string{"client_secret": "an interactive sign-in is a person's, and takes no secret"},
	},
	AuthMethodDeviceCode: {
		allowed: keySet("tenant_id", "client_id"),
		hints:   map[string]string{"client_secret": "a device-code sign-in is a person's, and takes no secret"},
	},
	AuthMethodAzureCLI: {
		allowed: keySet("tenant_id"),
		hints: map[string]string{"client_id": "the Azure CLI signs in as the account `az login` chose; " +
			"use service_principal to sign in as an application"},
	},
	AuthMethodAzureDefault: {
		allowed: keySet("tenant_id"),
		hints: map[string]string{"client_id": "DefaultAzureCredential takes a user-assigned managed " +
			"identity's client ID from the AZURE_CLIENT_ID environment variable"},
	},
	AuthMethodServicePrincipal: {
		allowed: keySet("tenant_id", "client_id", "client_secret", "certificate_path",
			"certificate_password", "send_certificate_chain"),
		extra: checkServicePrincipal,
	},
}

func keySet(keys ...string) map[string]bool {
	set := map[string]bool{"scope": true, "login_timeout": true}
	for _, key := range keys {
		set[key] = true
	}
	return set
}

// check refuses what method cannot honour: the top-level password, which no token method
// sends, and any auth key the method does not take.
func (r methodRules) check(method string, cfg dsconfig.DataSource) error {
	if cfg.Password != "" {
		return fmt.Errorf("sqlserver: password does not apply to auth.method %s, which signs in with an Entra ID "+
			"token; remove it (a service principal's secret goes in auth.client_secret)", method)
	}
	for _, key := range setAuthKeys(cfg.Auth) {
		if r.allowed[key] {
			continue
		}
		msg := fmt.Sprintf("sqlserver: auth.%s does not apply to auth.method %s", key, method)
		if hint, ok := r.hints[key]; ok {
			msg += "; " + hint
		}
		return errors.New(msg)
	}
	if r.extra != nil {
		return r.extra(cfg.Auth)
	}
	return nil
}

func checkServicePrincipal(a dsconfig.Auth) error {
	const method = "auth.method service_principal"
	switch {
	case a.TenantID == "":
		return errors.New("sqlserver: auth.tenant_id is required for " + method)
	case a.ClientID == "":
		return errors.New("sqlserver: auth.client_id is required for " + method)
	case a.ClientSecret == "" && a.CertificatePath == "":
		return errors.New("sqlserver: " + method + " needs auth.client_secret or auth.certificate_path; " +
			"an unset ${VAR} placeholder in the config reads as empty")
	case a.ClientSecret != "" && a.CertificatePath != "":
		return errors.New("sqlserver: " + method + " takes auth.client_secret or auth.certificate_path, not both")
	case a.CertificatePassword != "" && a.CertificatePath == "":
		return errors.New("sqlserver: auth.certificate_password decrypts auth.certificate_path, which is not set")
	case a.SendCertificateChain && a.CertificatePath == "":
		return errors.New("sqlserver: auth.send_certificate_chain applies to a certificate, and auth.certificate_path is not set")
	}
	return nil
}

// pendingMethods are method names that will exist but do not yet, each with what to use
// meanwhile.
var pendingMethods = map[string]string{
	"managed_identity": "sqlserver: auth.method managed_identity is not available yet; use azure_default, " +
		"which includes managed identity (select a user-assigned one with the AZURE_CLIENT_ID environment " +
		"variable), until the dedicated method ships",
	"workload_identity": "sqlserver: auth.method workload_identity is not available yet; use azure_default, " +
		"which includes workload identity, until the dedicated method ships",
}

// methodAliases map what someone arriving from a go-mssqldb or ADO.NET connection string, or
// an identity SDK, writes for a method to ours. They are suggestions, not aliases the config
// accepts: one spelling per method keeps configs greppable. Edit distance cannot catch these,
// since ActiveDirectoryInteractive is nowhere near interactive by letters.
var methodAliases = map[string]string{
	"activedirectoryinteractive":      AuthMethodInteractive,
	"activedirectorydevicecode":       AuthMethodDeviceCode,
	"devicecode":                      AuthMethodDeviceCode,
	"activedirectoryazcli":            AuthMethodAzureCLI,
	"azcli":                           AuthMethodAzureCLI,
	"az_cli":                          AuthMethodAzureCLI,
	"azurecli":                        AuthMethodAzureCLI,
	"cli":                             AuthMethodAzureCLI,
	"activedirectorydefault":          AuthMethodAzureDefault,
	"default":                         AuthMethodAzureDefault,
	"azuredefault":                    AuthMethodAzureDefault,
	"defaultazurecredential":          AuthMethodAzureDefault,
	"activedirectoryserviceprincipal": AuthMethodServicePrincipal,
	"activedirectoryapplication":      AuthMethodServicePrincipal,
	"serviceprincipal":                AuthMethodServicePrincipal,
	"application":                     AuthMethodServicePrincipal,
	"client_secret":                   AuthMethodServicePrincipal,
	"client_credentials":              AuthMethodServicePrincipal,
	"activedirectorymanagedidentity":  "managed_identity",
	"activedirectorymsi":              "managed_identity",
	"managedidentity":                 "managed_identity",
	"msi":                             "managed_identity",
	"activedirectoryworkloadidentity": "workload_identity",
	"workloadidentity":                "workload_identity",
	"sqlpassword":                     AuthMethodSQL,
	"sql_password":                    AuthMethodSQL,
	"sqllogin":                        AuthMethodSQL,
	"sql_login":                       AuthMethodSQL,
	"password":                        AuthMethodSQL,
}

// refusedMethods are sign-ins the engine does not offer at all, each with why.
var refusedMethods = map[string]string{
	"activedirectorypassword": "an Entra ID sign-in with a user's password is not supported: it cannot " +
		"satisfy MFA or Conditional Access; use interactive, device_code or azure_cli for a person, " +
		"service_principal for an application",
	"activedirectoryintegrated": "integrated (Windows or Kerberos) sign-in is not supported; use a SQL login or an Entra ID method",
	"integrated":                "integrated (Windows or Kerberos) sign-in is not supported; use a SQL login or an Entra ID method",
	"kerberos":                  "integrated (Windows or Kerberos) sign-in is not supported; use a SQL login or an Entra ID method",
	"windows":                   "integrated (Windows or Kerberos) sign-in is not supported; use a SQL login or an Entra ID method",
}

// validMethods lists every method auth.method can name in this binary: sql, the built-in
// token methods whether registered or not, and whatever else is registered.
func validMethods() []string {
	names := map[string]bool{AuthMethodSQL: true}
	for name := range tokenMethodRules {
		names[name] = true
	}
	for _, name := range RegisteredAuthMethods() {
		names[name] = true
	}
	list := make([]string, 0, len(names))
	for name := range names {
		list = append(list, name)
	}
	sort.Strings(list)
	return list
}

func unknownMethodError(method string) error {
	valid := validMethods()
	msg := fmt.Sprintf("sqlserver: unknown auth.method %q; valid: %s", method, strings.Join(valid, ", "))
	if why, ok := refusedMethods[method]; ok {
		return errors.New(msg + " — " + why)
	}
	if suggestion, ok := methodAliases[method]; ok {
		if _, pending := pendingMethods[suggestion]; pending {
			return fmt.Errorf("%s — did you mean %q? It is not available yet; use %q, which includes it, "+
				"until it ships", msg, suggestion, AuthMethodAzureDefault)
		}
		return fmt.Errorf("%s — did you mean %q?", msg, suggestion)
	}
	if suggestion, ok := dsconfig.Suggest(method, valid); ok {
		return fmt.Errorf("%s — did you mean %q?", msg, suggestion)
	}
	return errors.New(msg)
}
