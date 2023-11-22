package core

// HTTP

type Method string

const (
	GET    Method = "GET"
	POST          = "POST"
	PUT           = "PUT"
	DELETE        = "DELETE"
)

// Gorgany ORM

const GorganyORMTag = "grgorm"
const GorganyORMPreload = "preload"
const GorganyORMExtends = "extends"

const MessageContextKey = "messageContext"

const OneTimeParamsCookieName = "oneTimeParams"
const SessionCookieName = "sessionToken"
