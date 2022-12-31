package log

import (
	"go.uber.org/zap"
)

func MustZapLogger(debug bool) *zap.Logger {
	config := zap.NewProductionConfig()
	if debug {
		config.Level.SetLevel(zap.DebugLevel)
	}
	logger, err := config.Build()
	if err != nil {
		panic(err)
	}
	return logger
}
