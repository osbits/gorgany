package core

type ErrorHandler func(error, HttpMessage)

type HandlerFunc any
