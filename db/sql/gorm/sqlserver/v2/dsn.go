package v2

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

// DefaultPort is the port SQL Server listens on unless it is told otherwise, and the only one
// Azure SQL does.
const DefaultPort = 1433

// DefaultAppName is the application name a connection reports to the server, in
// sys.dm_exec_sessions and the audit log, unless options.app_name says otherwise.
const DefaultAppName = "gorgany"

// instanceName is what a SQL Server instance name may be: at most 16 characters of letters,
// digits, "_", "$", "#" and "-".
var instanceName = regexp.MustCompile(`^[A-Za-z0-9_$#-]{1,16}$`)

// endpoint is where a connection dials: a host and a port, or a host and a named instance
// whose port SQL Server Browser answers with.
type endpoint struct {
	host     string
	port     int
	instance string
}

// String is the endpoint as errors name it: host:port, or host\instance.
func (e endpoint) String() string {
	if e.instance != "" {
		return e.host + `\` + e.instance
	}
	return e.host + ":" + strconv.Itoa(e.port)
}

// resolveEndpoint decides where cfg dials, with the warnings the decision owes the reader.
//
// A named instance is dialled through SQL Server Browser (UDP 1434) only when it is the whole
// address, on premises with no port. That is JDBC's rule, and so DataGrip's and SSMS's: a set
// port wins and the instance is ignored, and a config copied from one of them with both set
// connects here as it does there — with a warning, since one of the two keys is dead. An Azure
// SQL host has no named instances and answers Browser on no port, so an instance is always
// dropped there, with a warning; sending it would fail a connection DataGrip makes.
//
// The host must be a host and nothing else. A port, an instance or a protocol written into it
// (host:1433, host,1433, host\instance, tcp:host — all of which connection strings allow) is
// refused with the key it belongs in, rather than half-parsed.
func resolveEndpoint(cfg dsconfig.DataSource) (endpoint, []string, error) {
	if err := checkHost(cfg.Host); err != nil {
		return endpoint{}, nil, err
	}
	e := endpoint{host: cfg.Host, port: cfg.Port}
	instance := strings.TrimSpace(cfg.Instance)
	if instance != "" && !instanceName.MatchString(instance) {
		return endpoint{}, nil, fmt.Errorf("sqlserver: instance %q is not an instance name: at most 16 "+
			"letters, digits, '_', '$', '#' or '-'", instance)
	}

	var warnings []string
	switch {
	case instance == "":
	case dsconfig.IsAzureSQLHost(cfg.Host):
		warnings = append(warnings, fmt.Sprintf("sqlserver: instance %q is ignored for %s: Azure SQL has no "+
			"named instances, so the connection goes to port %d; remove the instance key", instance, cfg.Host,
			portOr(cfg.Port)))
	case cfg.Port != 0:
		warnings = append(warnings, fmt.Sprintf("sqlserver: instance %q is ignored because port %d is set, as "+
			"JDBC, DataGrip and SSMS ignore it; remove one of the two keys", instance, cfg.Port))
	default:
		e.instance = instance
	}
	if e.instance == "" {
		e.port = portOr(cfg.Port)
	}
	return e, warnings, nil
}

func portOr(port int) int {
	if port == 0 {
		return DefaultPort
	}
	return port
}

// checkHost refuses a host that carries more than a host name or address.
func checkHost(host string) error {
	switch {
	case strings.HasPrefix(strings.ToLower(host), "tcp:"):
		return errors.New("sqlserver: host must not start with tcp:, which is connection-string syntax; " +
			"write the host name alone, and the port in port")
	case strings.ContainsAny(host, ":,"):
		return errors.New("sqlserver: host must be a host name or an IPv4 address alone; write the port in " +
			"port, not host:port or host,port")
	case strings.Contains(host, `\`):
		return errors.New(`sqlserver: host must be a host name alone; write a named instance in instance, ` +
			`not host\instance`)
	case strings.ContainsAny(host, "/@?#[] \t\r\n"):
		return errors.New("sqlserver: host must be a host name or an IPv4 address alone")
	}
	return nil
}

// describe names cfg's server and database the way every error this engine returns does:
// host:port/db, or host\instance/db. It is the only form a connection's address takes in an
// error or a log line, since the DSN carries the password.
func describe(cfg dsconfig.DataSource) string {
	e, _, err := resolveEndpoint(cfg)
	if err != nil {
		e = endpoint{host: cfg.Host, port: portOr(cfg.Port)}
	}
	return e.String() + "/" + cfg.Database
}

// sslToEncrypt maps the ssl key to go-mssqldb's encrypt parameter.
//
// An empty ssl means encrypted, not go-mssqldb's own default, which encrypts only the login
// and trusts any certificate: an unresolved ${VAR} placeholder boots as "", and that must not
// be what weakens a connection. The names Postgres and MySQL use for "off" and "on" map onto
// go-mssqldb's, and strict is TDS 8.0, which Azure SQL and SQL Server 2022 speak.
var sslToEncrypt = map[string]string{
	"":          "true",
	"true":      "true",
	"mandatory": "true",
	"yes":       "true",
	"1":         "true",
	"strict":    "strict",
	"false":     "false",
	"optional":  "false",
	"no":        "false",
	"0":         "false",
	"disable":   "disable",
}

// encryptFor returns go-mssqldb's encrypt value for cfg.SSL, refusing a weaker one on Azure,
// and for a token sign-in anywhere.
//
// A token sign-in sends its Entra ID access token in the login, and with encrypt false the
// login is encrypted only if the server says it can be: a server, or anything in between, that
// says it cannot receives the token in clear. The token is a bearer credential for its audience,
// which for Azure SQL is every database the principal can reach, in every server, so it must
// never travel unencrypted, whatever the host is called; a private endpoint behind a DNS name of
// its own is still Azure SQL.
func encryptFor(cfg dsconfig.DataSource, azure, token bool) (string, error) {
	ssl := strings.ToLower(strings.TrimSpace(cfg.SSL))
	encrypt, ok := sslToEncrypt[ssl]
	if !ok {
		return "", fmt.Errorf("sqlserver: ssl %q is not a SQL Server encryption mode; use true (the default), "+
			"strict, false or disable", cfg.SSL)
	}
	weak := encrypt == "false" || encrypt == "disable"
	switch {
	case azure && weak:
		return "", fmt.Errorf("sqlserver: ssl %q would send an Azure SQL connection unencrypted, which Azure "+
			"refuses; remove ssl, or set it to true or strict", cfg.SSL)
	case token && weak:
		return "", fmt.Errorf("sqlserver: ssl %q would let an Entra ID sign-in send its access token "+
			"unencrypted, and the token lets whoever reads it into every database the principal can reach; "+
			"remove ssl, or set it to true or strict", cfg.SSL)
	}
	return encrypt, nil
}

// BuildDSN renders cfg as a go-mssqldb URL DSN:
//
//	sqlserver://user:password@host:port/instance?database=…&encrypt=…&…
//
// It is exported for tests. Never log what it returns: it carries the password. Errors name
// the server with describe instead.
//
// It is built with net/url, so a login such as user@example.com, a password with ":" or "@"
// in it, and a hyphenated database such as Example-db are escaped for their positions, and no
// option value can smuggle in a second parameter. Parameters are sorted, so the DSN is
// byte-for-byte reproducible.
//
// What it sets:
//
//   - database is db, as written.
//   - encrypt comes from ssl (see sslToEncrypt), and trustservercertificate is always sent,
//     false unless options.trust_server_certificate says true. go-mssqldb trusts any
//     certificate when neither is set. An Azure SQL host and an Entra ID sign-in on any host
//     refuse encryption turned off and a trusted certificate, and strict refuses a trusted
//     certificate anywhere.
//   - app name is DefaultAppName, and guid conversion is true, so a uniqueidentifier scans
//     into a uuid.UUID in the byte order it is shown in; an option overrides either. The
//     conversion is wrong for go-mssqldb's own mssql.UniqueIdentifier and
//     NullUniqueIdentifier, which reorder the bytes themselves: with it on they read every
//     GUID with its first three groups reversed and bind one that matches nothing, without an
//     error. Use uuid.UUID and uuid.NullUUID, or set options.guid_conversion: false for code
//     built on go-mssqldb's types.
//   - applicationintent is ReadOnly when read_only is set, which routes the connection to a
//     readable secondary where there is one. options.application_intent overrides it:
//     ReadWrite is allowed with read_only, which only asks for the primary, while ReadOnly
//     without read_only is refused, since it would route writes to a node that refuses them.
//   - The username and password go in the URL only for the SQL login; a token method signs
//     in with a token, and its username is at most a hint to the identity provider.
//
// search_path is refused (see refuseSearchPath).
func BuildDSN(cfg dsconfig.DataSource) (string, error) {
	dsn, _, _, err := buildDSN(cfg, authMethodOf(cfg) == AuthMethodSQL)
	return dsn, err
}

// buildDSN is BuildDSN with the endpoint it dials and the warnings owed for it. sqlLogin says
// whether the credentials go in the URL.
func buildDSN(cfg dsconfig.DataSource, sqlLogin bool) (string, endpoint, []string, error) {
	if err := refuseSearchPath(cfg); err != nil {
		return "", endpoint{}, nil, err
	}

	e, warnings, err := resolveEndpoint(cfg)
	if err != nil {
		return "", endpoint{}, nil, err
	}
	options, err := canonicalOptions(cfg)
	if err != nil {
		return "", endpoint{}, nil, err
	}

	azure := dsconfig.IsAzureSQLHost(cfg.Host)
	encrypt, err := encryptFor(cfg, azure, !sqlLogin)
	if err != nil {
		return "", endpoint{}, nil, err
	}

	params := url.Values{}
	params.Set("database", cfg.Database)
	params.Set("encrypt", encrypt)
	params.Set("app name", DefaultAppName)
	params.Set("guid conversion", "true")
	params.Set("trustservercertificate", "false")
	if cfg.ReadOnly {
		params.Set("applicationintent", intentReadOnly)
	}
	for key, values := range options {
		params[key] = values
	}

	if params.Get("trustservercertificate") == "true" {
		switch {
		case azure:
			return "", endpoint{}, nil, errors.New("sqlserver: options.trust_server_certificate would accept any " +
				"certificate from an Azure SQL host, whose certificate always verifies; remove it")
		case encrypt == "strict":
			return "", endpoint{}, nil, errors.New("sqlserver: options.trust_server_certificate cannot be used with " +
				"ssl strict, which verifies the certificate by definition")
		case !sqlLogin:
			return "", endpoint{}, nil, errors.New("sqlserver: options.trust_server_certificate would hand an Entra " +
				"ID sign-in's access token to any server that answers, since it accepts any certificate; verify " +
				"the server instead, with options.certificate or options.server_certificate for a self-signed " +
				"certificate, or options.hostname_in_certificate for a name that differs from host")
		}
	}
	if params.Get("applicationintent") == intentReadOnly && !cfg.ReadOnly {
		return "", endpoint{}, nil, errors.New("sqlserver: options.application_intent ReadOnly routes the " +
			"connection to a read-only node without read_only: true, so every write would fail there; " +
			"set read_only: true, or remove the option")
	}

	u := url.URL{Scheme: "sqlserver", RawQuery: encodeSorted(params)}
	if e.instance != "" {
		u.Host = e.host
		u.Path = "/" + e.instance
	} else {
		u.Host = e.host + ":" + strconv.Itoa(e.port)
	}
	if sqlLogin && cfg.Username != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}
	return u.String(), e, warnings, nil
}

// refuseSearchPath refuses search_path, which SQL Server has no counterpart for: an
// unqualified name resolves against the login's default schema, which no connection parameter
// changes, so honouring the key is impossible and ignoring it would connect to the wrong
// tables.
func refuseSearchPath(cfg dsconfig.DataSource) error {
	if cfg.SearchPath == "" {
		return nil
	}
	return dbCore.Unsupported(DialectName, "search_path",
		`SQL Server has no schema search path; qualify other schemas in TableName(), e.g. "sales.Orders"`)
}

// encodeSorted encodes params with keys in lexical order, so the DSN is byte-for-byte
// reproducible.
func encodeSorted(params url.Values) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, url.QueryEscape(k)+"="+url.QueryEscape(params.Get(k)))
	}
	return strings.Join(pairs, "&")
}
