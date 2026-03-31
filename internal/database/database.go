package database

import (
	"database/sql"
	"fmt"
	"log"
	"time"
	"vendia-backend/internal/config"
	"vendia-backend/internal/models"

	"github.com/pressly/goose/v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Connect(cfg *config.Config) (*gorm.DB, error) {
	gormCfg := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	}

	var db *gorm.DB
	var err error
	for attempt := 1; attempt <= 5; attempt++ {
		db, err = gorm.Open(postgres.Open(cfg.DatabaseURL), gormCfg)
		if err == nil {
			break
		}
		log.Printf("[DB] attempt %d/5 failed: %v — retrying in %ds...", attempt, err, attempt*2)
		time.Sleep(time.Duration(attempt*2) * time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("could not connect to database after 5 attempts: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(5)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(4 * time.Minute)
	sqlDB.SetConnMaxIdleTime(90 * time.Second)

	log.Println("[DB] connection established")
	return db, nil
}

func Migrate(db *gorm.DB) error {
	log.Println("[DB] running auto-migrations...")

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	if _, err := sqlDB.Exec(`CREATE EXTENSION IF NOT EXISTS "pgcrypto"`); err != nil {
		log.Printf("[DB] warning: could not create pgcrypto extension: %v", err)
	}

	err = db.AutoMigrate(
		&models.Tenant{},
		&models.Employee{},
		&models.Product{},
		&models.Sale{},
		&models.SaleItem{},
		&models.RefreshToken{},
		&models.Customer{},
		&models.CreditAccount{},
		&models.CreditPayment{},
		&models.Table{},
		&models.OpenTab{},
		&models.AdminUser{},
		&models.Supplier{},
		&models.OrderTicket{},
		&models.OrderItem{},
		&models.Recipe{},
		&models.RecipeIngredient{},
		&models.Promotion{},
		&models.RockolaSuggestion{},
		&models.CatalogProduct{},
	)
	if err != nil {
		return err
	}
	log.Println("[DB] auto-migrations completed")
	return nil
}

func RunGooseMigrations(sqlDB *sql.DB, migrationsDir string) error {
	log.Println("[DB] running goose migrations...")
	goose.SetBaseFS(nil)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}
	if err := goose.Up(sqlDB, migrationsDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	log.Println("[DB] goose migrations completed")
	return nil
}
