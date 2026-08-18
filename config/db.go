package config

import (
	"fmt"
	"log"
	"os"

	"goapi/models"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var DB *gorm.DB

func ConnectDB() {
	dsn := fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		os.Getenv("DB_USER"),
		os.Getenv("DB_PASS"),
		os.Getenv("DB_HOST"),
		os.Getenv("DB_PORT"),
		os.Getenv("DB_NAME"),
	)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	DB = db
	log.Println("Database connected")
}

// MigrateDB creates the driver_assignments table if it doesn't exist.
// It does NOT touch your existing Laravel tables.
func MigrateDB() {
	err := DB.AutoMigrate(&models.DriverAssignment{})
	if err != nil {
		log.Fatal("Migration failed:", err)
	}
	log.Println("Migration done")
}
