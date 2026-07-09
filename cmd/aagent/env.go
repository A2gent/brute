package main

import (
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

// loadDotEnv loads .env files from common locations. Missing files are ignored.
func loadDotEnv() {
	homeDir, _ := os.UserHomeDir()
	godotenv.Load(".env")
	godotenv.Load(filepath.Join(homeDir, ".env"))
	godotenv.Load(filepath.Join(homeDir, "git/mind/.env"))
}
