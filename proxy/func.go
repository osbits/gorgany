package proxy

type ErrorHandler func(error, HttpMessage)

type HandlerFunc any
