// http/httpsession_scope.go
package http

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorganyio/gorgany/app/core"
)

// HTTPSessionScope manages session lifecycle and one-time values.
// Scope lifetime: per request.
type HTTPSessionScope struct {
	AuthStrategy   core.IAuthContext    `container:"inject"`
	Storage        core.ISessionStorage `container:"inject"`
	Request        *http.Request
	Writer         http.ResponseWriter
	currentSession core.ISession
}

// NewHTTPSessionScope constructs a HTTPSessionScope.
func NewHTTPSessionScope(w http.ResponseWriter, r *http.Request) *HTTPSessionScope {
	sessionScope := &HTTPSessionScope{Writer: w, Request: r}
	sessionScope.initCurrentSession()
	return sessionScope
}

func (sc *HTTPSessionScope) initCurrentSession() {
	strat := sc.AuthStrategy.ResolveAuthStrategyByContext(sc.Request.Context())
	if strat == nil {
		return
	}

	// try to load existing session
	sid := strat.ResolveSessionId(sc.Request.Context())
	if sid != "" {
		if sess := sc.Storage.GetSessionById(sid); sess != nil {
			sc.currentSession = sess
			return
		}
	}
	// create a new session if none found
	newSess, err := strat.NewSessionWithoutUser(sc.Request.Context())
	if err != nil {
		panic(fmt.Errorf("HTTPSessionScope.GetSession: cannot create session: %v", err))
	}
	sc.currentSession = newSess
}

// GetSession retrieves the existing session or creates a new one if none exists.
func (sc *HTTPSessionScope) GetSession() core.ISession {
	if sc.currentSession != nil {
		return sc.currentSession
	}
	strat := sc.AuthStrategy.ResolveAuthStrategyByContext(sc.Request.Context())
	if strat == nil {
		return nil
	}
	// try to load existing session
	sid := strat.ResolveSessionId(sc.Request.Context())
	if sid != "" {
		if sess := sc.Storage.GetSessionById(sid); sess != nil {
			sc.currentSession = sess
			return sess
		}
	}
	// create a new session if none found
	newSess, err := strat.NewSessionWithoutUser(sc.Request.Context())
	if err != nil {
		panic(fmt.Errorf("HTTPSessionScope.GetSession: cannot create session: %v", err))
	}
	sc.currentSession = newSess
	return newSess
}

// SaveOneTime stores a one-time value under the given key.
func (sc *HTTPSessionScope) SaveOneTime(key string, value any) {
	sess := sc.GetSession()
	if sess == nil {
		return
	}
	buf, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	sess.SetItem(key, string(buf))
}

// GetOneTime reads a one-time value into v.
func (sc *HTTPSessionScope) GetOneTime(key string, v any) error {
	sess := sc.GetSession()
	if sess == nil {
		return nil
	}
	raw := sess.GetItem(key)
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), v)
}

// ClearOneTime removes a one-time value after it has been used.
func (sc *HTTPSessionScope) ClearOneTime() {
	if sess := sc.GetSession(); sess != nil {
		sess.ClearItem(core.OneTimeSessionAttributeKey)
	}
}
