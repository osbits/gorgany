package v2

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
)

// optionSpec is one go-mssqldb connection parameter databases.<name>.options may set: the key
// go-mssqldb reads, and how its value is checked and spelled for it.
type optionSpec struct {
	driverKey string
	value     func(v string) (string, bool)
	// expect describes a valid value, for the error that refuses an invalid one.
	expect string
}

// optionSpecs are the options this engine passes to go-mssqldb, by their canonical,
// snake_case name.
//
// The list is closed. go-mssqldb ignores a key it does not know, so a misspelt option would
// otherwise do nothing and say nothing — and the ones that matter here are about encryption
// and certificates, where "nothing" means a weaker connection than the one configured.
//
// Several of go-mssqldb's own keys contain a space ("app name", "dial timeout"), which a
// YAML key can hold but few people write, so each option is also accepted in snake_case;
// optionSpellings has every spelling. Keys that typed config owns are refused, not accepted
// twice (see ownedOptions).
var optionSpecs = map[string]optionSpec{
	"app_name":                 text("app name", "a name"),
	"dial_timeout":             seconds("dial timeout"),
	"connection_timeout":       seconds("connection timeout"),
	"keep_alive":               seconds("keepalive"),
	"packet_size":              {driverKey: "packet size", value: unsigned(16), expect: "a whole number of bytes"},
	"workstation_id":           text("workstation id", "a name"),
	"trust_server_certificate": flag("trustservercertificate"),
	"hostname_in_certificate":  text("hostnameincertificate", "a host name"),
	"certificate":              text("certificate", "a file path"),
	"server_certificate":       text("servercertificate", "a file path"),
	"tls_min":                  choice("tlsmin", "1.0", "1.1", "1.2", "1.3"),
	"server_spn":               text("serverspn", "a service principal name"),
	"failover_partner":         text("failoverpartner", "a host name"),
	"failover_port":            {driverKey: "failoverport", value: unsigned(16), expect: "a port number"},
	"failover_partner_spn":     text("failoverpartnerspn", "a service principal name"),
	"multi_subnet_failover":    flag("multisubnetfailover"),
	"disable_retry":            flag("disableretry"),
	"protocol":                 choice("protocol", "tcp", "admin", "np", "lpc"),
	"pipe":                     text("pipe", "a pipe name"),
	"no_trace_id":              flag("notraceid"),
	"timezone":                 {driverKey: "timezone", value: timezone, expect: "an IANA time zone such as Europe/Zurich"},
	"epa_enabled":              flag("epa enabled"),
	"guid_conversion":          flag("guid conversion"),
	"log":                      {driverKey: "log", value: unsigned(64), expect: "go-mssqldb's log flags, a whole number"},
	"application_intent":       choice("applicationintent", intentReadOnly, intentReadWrite),
}

func text(driverKey, expect string) optionSpec {
	return optionSpec{driverKey: driverKey, value: anyText, expect: expect}
}

func seconds(driverKey string) optionSpec {
	return optionSpec{driverKey: driverKey, value: unsigned(64), expect: "a whole number of seconds"}
}

func flag(driverKey string) optionSpec {
	return optionSpec{driverKey: driverKey, value: boolean, expect: "a boolean (true, false, yes or no)"}
}

func choice(driverKey string, values ...string) optionSpec {
	expect := strings.Join(values[:len(values)-1], ", ") + " or " + values[len(values)-1]
	return optionSpec{driverKey: driverKey, value: oneOf(values...), expect: expect}
}

// optionSpellings maps every accepted spelling of an option, lowercased, to its canonical
// name: the snake_case name itself, go-mssqldb's own key, and the ADO.NET and snake_case
// variants people copy from connection strings.
var optionSpellings = func() map[string]string {
	spellings := map[string]string{}
	for canonical, spec := range optionSpecs {
		spellings[canonical] = canonical
		spellings[spec.driverKey] = canonical
	}
	for spelling, canonical := range map[string]string{
		"application_name":         "app_name",
		"application name":         "app_name",
		"appname":                  "app_name",
		"connect_timeout":          "connection_timeout",
		"connect timeout":          "connection_timeout",
		"packetsize":               "packet_size",
		"wsid":                     "workstation_id",
		"trust server certificate": "trust_server_certificate",
		"host name in certificate": "hostname_in_certificate",
		"host_name_in_certificate": "hostname_in_certificate",
		"ca_certificate":           "certificate",
		"server certificate":       "server_certificate",
		"server spn":               "server_spn",
		"failover partner":         "failover_partner",
		"failover partner spn":     "failover_partner_spn",
		"multi subnet failover":    "multi_subnet_failover",
		"time_zone":                "timezone",
		"driver_log":               "log",
		"application intent":       "application_intent",
	} {
		spellings[spelling] = canonical
	}
	return spellings
}()

// ownedOptions are go-mssqldb keys that typed config owns, each with the key to use instead.
// Accepting them here as well would give a setting two sources and no rule for which wins.
var ownedOptions = map[string]string{
	"database":        "db",
	"initial catalog": "db",
	"initial_catalog": "db",
	"user id":         "username",
	"user_id":         "username",
	"userid":          "username",
	"user":            "username",
	"uid":             "username",
	"password":        "password",
	"pwd":             "password",
	"server":          "host (and instance)",
	"data source":     "host (and instance)",
	"data_source":     "host (and instance)",
	"address":         "host (and instance)",
	"addr":            "host (and instance)",
	"network address": "host (and instance)",
	"port":            "port",
	"instance":        "instance",
	"encrypt":         "ssl",
	"fedauth":         "auth",
	"authentication":  "auth",

	"applicationclientid":        "auth",
	"clientcertpath":             "auth",
	"resource id":                "auth",
	"tenant id":                  "auth",
	"serviceconnectionid":        "auth",
	"systemtoken":                "auth",
	"clientassertion":            "auth",
	"userassertion":              "auth",
	"tokenfilepath":              "auth",
	"sendcertificatechain":       "auth",
	"additionallyallowedtenants": "auth",
	"disableinstancediscovery":   "auth",
}

// unsupportedOptions are go-mssqldb features the engine refuses, each with why.
var unsupportedOptions = map[string]string{
	"change password":           changePasswordRefusal,
	"change_password":           changePasswordRefusal,
	"columnencryption":          alwaysEncryptedRefusal,
	"column_encryption":         alwaysEncryptedRefusal,
	"column encryption setting": alwaysEncryptedRefusal,
}

const (
	changePasswordRefusal  = "gorgany never changes a login's password"
	alwaysEncryptedRefusal = "Always Encrypted is not supported: the ORM would read ciphertext it cannot write back"
)

// canonicalOptions checks cfg.Options and returns them keyed by go-mssqldb's own names, values
// spelled as go-mssqldb reads them.
//
// Keys are matched lowercased, trimmed and with runs of spaces folded to one, so a map built
// by hand in camel case means what the same map read from YAML does. Every key must be one
// optionSpellings knows; one that typed config owns, one this engine does not support and one
// it does not know are each refused with what to do instead, and two spellings of one option
// are refused as a duplicate. An error names the option as written and never its value.
func canonicalOptions(cfg dsconfig.DataSource) (url.Values, error) {
	params := url.Values{}
	written := map[string]string{} // canonical name -> key as written
	var unknown []string

	for _, key := range cfg.SortedOptionKeys() {
		spelling := foldOptionKey(key)
		if owner, ok := ownedOptions[spelling]; ok {
			return nil, fmt.Errorf("sqlserver: options.%s is set with the typed key %s, not in options", key, owner)
		}
		if why, ok := unsupportedOptions[spelling]; ok {
			return nil, unsupported("options."+key, why)
		}
		canonical, ok := optionSpellings[spelling]
		if !ok {
			unknown = append(unknown, key)
			continue
		}
		if earlier, dup := written[canonical]; dup {
			return nil, fmt.Errorf("sqlserver: options.%s and options.%s both set %s; keep one", earlier, key, canonical)
		}
		written[canonical] = key

		spec := optionSpecs[canonical]
		raw := strings.TrimSpace(cfg.Options[key])
		if raw == "" {
			return nil, fmt.Errorf("sqlserver: options.%s is empty; remove it or give it a value "+
				"(an unset ${VAR} placeholder in the config reads as empty)", key)
		}
		value, ok := spec.value(raw)
		if !ok {
			return nil, fmt.Errorf("sqlserver: options.%s must be %s", key, spec.expect)
		}
		params.Set(spec.driverKey, value)
	}

	if len(unknown) > 0 {
		return nil, unknownOptionsError(unknown)
	}
	return params, nil
}

// foldOptionKey is key as optionSpellings is keyed: lowercased, trimmed, spaces folded.
func foldOptionKey(key string) string {
	return strings.Join(strings.Fields(strings.ToLower(key)), " ")
}

func unknownOptionsError(unknown []string) error {
	quoted := make([]string, len(unknown))
	for i, key := range unknown {
		quoted[i] = "'" + key + "'"
	}
	parts := []string{fmt.Sprintf("sqlserver: unknown option(s) %s under options", strings.Join(quoted, ", "))}

	spellings := make([]string, 0, len(optionSpellings))
	for spelling := range optionSpellings {
		spellings = append(spellings, spelling)
	}
	for _, key := range unknown {
		if match, ok := dsconfig.Suggest(foldOptionKey(key), spellings); ok {
			parts = append(parts, fmt.Sprintf("did you mean '%s' instead of '%s'?", optionSpellings[match], key))
		}
	}

	canonical := make([]string, 0, len(optionSpecs))
	for name := range optionSpecs {
		canonical = append(canonical, name)
	}
	sort.Strings(canonical)
	parts = append(parts, "recognised options are "+strings.Join(canonical, ", "))
	return errors.New(strings.Join(parts, " — "))
}

func anyText(v string) (string, bool) { return v, true }

// boolean accepts what go-mssqldb does — strconv.ParseBool's spellings, and yes and no — and
// spells the value true or false.
func boolean(v string) (string, bool) {
	switch strings.ToLower(v) {
	case "yes":
		return "true", true
	case "no":
		return "false", true
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return "", false
	}
	return strconv.FormatBool(parsed), true
}

func unsigned(bits int) func(string) (string, bool) {
	return func(v string) (string, bool) {
		parsed, err := strconv.ParseUint(v, 10, bits)
		if err != nil {
			return "", false
		}
		return strconv.FormatUint(parsed, 10), true
	}
}

func oneOf(values ...string) func(string) (string, bool) {
	return func(v string) (string, bool) {
		for _, allowed := range values {
			if strings.EqualFold(v, allowed) {
				return allowed, true
			}
		}
		return "", false
	}
}

func timezone(v string) (string, bool) {
	if _, err := time.LoadLocation(v); err != nil {
		return "", false
	}
	return v, true
}

// The two ApplicationIntent values. go-mssqldb compares the value case-sensitively and treats
// anything but "ReadOnly" as read-write, so "readonly" would silently route to the primary.
// The value is therefore accepted in any case and sent in this one.
const (
	intentReadOnly  = "ReadOnly"
	intentReadWrite = "ReadWrite"
)
