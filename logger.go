package main

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Return simple logger with rotation. v1.
// Take logging level, full path to log file, vax size of log file in MB and number of backup files.
// Have no time limit for store log files
func NewZapSimpleLoggerWithRotation(logLevelStr string, logFilePath string, maxSize, maxBackups int) *zap.Logger {
	var logLevel zapcore.Level
	err := logLevel.UnmarshalText([]byte(logLevelStr))
	if err != nil {
		logLevel = zapcore.ErrorLevel
	}

	var cfg zap.Config
	cfg.EncoderConfig.TimeKey = "time"
	cfg.EncoderConfig.MessageKey = "message"
	cfg.EncoderConfig.LevelKey = "level"
	cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("2006.01.02 15:04:05")
	cfg.EncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder

	writer := zapcore.AddSync(&lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    maxSize, // megabytes
		MaxBackups: maxBackups,
	})

	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(cfg.EncoderConfig),
		writer,
		logLevel,
	)
	logger := zap.New(core)

	return logger
}
