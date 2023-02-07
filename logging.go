package dtcp

import (
	"log"
)

type LogLevel int

const (
	LOG_DEBUG LogLevel = iota
	LOG_INFO
	LOG_WARN
	LOG_ERROR
	LOG_FATAL
)

var (
	logLevel LogLevel = LOG_ERROR
)

func SetLogLevel(l LogLevel) {
	// RACY!!!
	logLevel = l
}

func logAtLevel(lvl LogLevel, format string, v ...any) {
	if logLevel < lvl {
		return
	}
	log.Printf(format, v...)
}

func LogDebug(format string, v ...any) {
	logAtLevel(LOG_DEBUG, format, v...)
}

func LogInfo(format string, v ...any) {
	logAtLevel(LOG_INFO, format, v...)
}

func LogWarn(format string, v ...any) {
	logAtLevel(LOG_WARN, format, v...)
}

func LogError(format string, v ...any) {
	logAtLevel(LOG_ERROR, format, v...)
}

func LogFatal(format string, v ...any) {
	log.Fatalf(format, v...)
}
