package main

import (
	"log"
	"os"
)

type Log struct {
	Info log.Logger
	Warn log.Logger
	Error log.Logger
}
var logger Log = Log{
	Info:	*log.New(os.Stdout, "[INFO]\t", log.Ltime|log.Lshortfile),
	Warn:	*log.New(os.Stderr, "[WARN]\t", log.Ltime|log.Lshortfile),
	Error:	*log.New(os.Stderr, "[ERROR]\t", log.Ltime|log.Lshortfile),
}
