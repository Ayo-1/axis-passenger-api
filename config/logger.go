package config

import (
	"io"
	"log"
	"os"
)

func InitLogger() {
	logFile, err := os.OpenFile("/var/www/axis-api/app/logs/api.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Println("Failed to open log file, using stdout only:", err)
		return
	}

	// Write to both file and console
	multiWriter := io.MultiWriter(os.Stdout, logFile)
	log.SetOutput(multiWriter)
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	log.Println("=== API Started ===")
}
