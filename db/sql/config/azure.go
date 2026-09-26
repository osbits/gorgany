package config

import "strings"

// azureSQLHostSuffixes are the DNS suffixes Microsoft serves Azure SQL endpoints under, with
// the cloud each belongs to.
//
// They are listed here, in the engine-neutral config package, rather than in the SQL Server
// engine, because more than the engine has to recognise such a host: the test harness
// refuses to point at one, so a test run with a stray production variable cannot truncate
// a cloud database, and it must be able to say so without linking a SQL Server driver.
//
// Synapse and Fabric SQL endpoints speak the same protocol and take the same sign-in, so
// they are Azure SQL hosts for every purpose this answers.
var azureSQLHostSuffixes = []azureSuffix{
	{".database.windows.net", AzureCloudPublic},
	{".database.usgovcloudapi.net", AzureCloudUSGov},
	{".database.chinacloudapi.cn", AzureCloudChina},
	{".sql.azuresynapse.net", AzureCloudPublic},
	{".datawarehouse.fabric.microsoft.com", AzureCloudPublic},
	{".database.fabric.microsoft.com", AzureCloudPublic},
}

type azureSuffix struct {
	suffix string
	cloud  string
}

// The clouds AzureCloudOf reports. Each has its own sign-in authority and token audience,
// so a token acquired against one is refused by a server in another.
const (
	AzureCloudPublic = "public"
	AzureCloudUSGov  = "usgov"
	AzureCloudChina  = "china"
)

// IsAzureSQLHost reports whether host is an Azure SQL endpoint, such as
// example.database.windows.net.
//
// The match is on a whole DNS suffix with at least one label in front of it, compared
// case-insensitively and ignoring the trailing dot of a fully qualified name. Both halves of
// that matter. A substring test would call database.windows.net.example.com an Azure host,
// and one that accepted the bare suffix would call database.windows.net one; neither names
// a server. The answer is a safety decision for some callers — the test harness refuses an
// Azure host outright — so a host that merely resembles one must not pass for it, and one
// spelled in capitals or with its root dot must not slip past it.
//
// Nor may one written the way a connection string or a SQL client writes it. The Azure
// portal's ADO.NET string says Server=tcp:example.database.windows.net,1433, a JDBC-style
// host field takes host:1433 or host\instance, and the `server` key is pointed at `host`, so
// any of those can arrive here: Validate checks only that a host is set. So the name is read
// out of them first. A protocol prefix such as tcp: goes, as does a trailing ,port or :port,
// and everything from the first \ or /. A comma that is not followed by a port separates
// hosts, as it does in a Postgres host list, and the answer is yes when any host in the list
// is an Azure SQL host.
func IsAzureSQLHost(host string) bool {
	_, ok := azureSuffixOf(host)
	return ok
}

// AzureCloudOf reports which Azure cloud host belongs to: AzureCloudUSGov, AzureCloudChina
// or AzureCloudPublic.
//
// A host that is not an Azure SQL endpoint at all also answers AzureCloudPublic, since that
// is the authority an identity SDK uses when told nothing. Ask IsAzureSQLHost first when the
// difference matters.
func AzureCloudOf(host string) string {
	if entry, ok := azureSuffixOf(host); ok {
		return entry.cloud
	}
	return AzureCloudPublic
}

// azureSuffixOf returns the entry of the first host in host that is an Azure SQL host; see
// IsAzureSQLHost for how host is read.
func azureSuffixOf(host string) (azureSuffix, bool) {
	for _, name := range hostNames(host) {
		if entry, ok := azureSuffixOfName(name); ok {
			return entry, true
		}
	}
	return azureSuffix{}, false
}

// hostNames reads the DNS names out of host, lowercased: the one name a DataSource's Host
// normally is, or each of a comma-separated list, without the protocol prefix, port, instance
// or path a connection string attaches to it.
func hostNames(host string) []string {
	var names []string
	for _, part := range strings.Split(host, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if isPort(name) {
			continue // the ,1433 of tcp:host,1433
		}
		if cut := strings.IndexAny(name, `\/`); cut >= 0 {
			name = name[:cut]
		}
		if colon := strings.LastIndexByte(name, ':'); colon >= 0 && isPort(name[colon+1:]) {
			name = name[:colon]
		}
		// What is left of a colon now is a protocol prefix, tcp: or another an ADO.NET string
		// takes; a DNS name has no colon in it.
		name = name[strings.LastIndexByte(name, ':')+1:]
		names = append(names, strings.TrimRight(name, "."))
	}
	return names
}

// isPort reports whether s is a port number's digits.
func isPort(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func azureSuffixOfName(name string) (azureSuffix, bool) {
	for _, entry := range azureSQLHostSuffixes {
		prefix, found := strings.CutSuffix(name, entry.suffix)
		if !found {
			continue
		}
		// The label immediately in front of the suffix must be non-empty, so neither
		// ".database.windows.net" nor "a..database.windows.net" counts.
		if label := prefix[strings.LastIndex(prefix, ".")+1:]; label != "" {
			return entry, true
		}
	}

	return azureSuffix{}, false
}

// Suggest returns the candidate closest to input, when one is close enough to be a likely
// typo of it. Matching ignores case, and the candidate comes back spelled as given.
//
// It is the edit-distance half of the unknown-key suggestions, exported so an engine can
// make the same suggestion for vocabulary that is its own — the option keys and sign-in
// methods it recognises — with the same threshold, rather than each engine inventing a
// different idea of "close".
//
// The threshold is tight on purpose: at most a third of the shorter name may differ, and
// never more than two edits, so a name of fewer than three characters gets no suggestion at
// all. A wrong suggestion reads as authoritative and sends the reader to rename something
// that was never a typo. Ties go to the candidate that sorts first, so the answer does not
// depend on the order of candidates.
func Suggest(input string, candidates []string) (string, bool) {
	lowered := strings.ToLower(input)

	best := ""
	bestDistance := 0
	for _, candidate := range candidates {
		lowerCandidate := strings.ToLower(candidate)
		distance := editDistance(lowered, lowerCandidate)

		limit := min(len(lowerCandidate), len(lowered)) / 3
		if limit > 2 {
			limit = 2
		}
		if limit < 1 || distance > limit {
			continue
		}

		if best == "" || distance < bestDistance || (distance == bestDistance && candidate < best) {
			best, bestDistance = candidate, distance
		}
	}

	return best, best != ""
}
