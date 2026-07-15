package models

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestUserSingleSubjectFieldsPersist(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}

	subjectKey := SystemAdminSubjectKey
	now := time.Now().UTC().Truncate(time.Second)
	user := User{
		Username:          "admin",
		PasswordHash:      "hash",
		Role:              "admin",
		Enabled:           true,
		SubjectKey:        &subjectKey,
		SessionVersion:    1,
		PasswordChangedAt: &now,
		LastLoginAt:       &now,
		InitializedAt:     &now,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	var got User
	if err := db.First(&got, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.SubjectKey == nil || *got.SubjectKey != SystemAdminSubjectKey {
		t.Fatalf("subject key = %v", got.SubjectKey)
	}
	if got.SessionVersion != 1 {
		t.Fatalf("session version = %d, want 1", got.SessionVersion)
	}
	if got.PasswordChangedAt == nil || got.LastLoginAt == nil || got.InitializedAt == nil {
		t.Fatal("security timestamps were not persisted")
	}
}

func TestUserAllowsRetiredRowsWithoutSecondActiveSubject(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}

	subjectKey := SystemAdminSubjectKey
	active := User{Username: "admin", PasswordHash: "hash", Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1}
	if err := db.Create(&active).Error; err != nil {
		t.Fatal(err)
	}

	retiredAt := time.Now().UTC()
	retired := User{Username: "retired", PasswordHash: "hash", Role: "viewer", Enabled: false, RetiredAt: &retiredAt}
	if err := db.Create(&retired).Error; err != nil {
		t.Fatalf("retired row should coexist: %v", err)
	}

	secondKey := SystemAdminSubjectKey
	second := User{Username: "second", PasswordHash: "hash", Role: "admin", Enabled: true, SubjectKey: &secondKey, SessionVersion: 1}
	if err := db.Create(&second).Error; err == nil {
		t.Fatal("second active system_admin subject should be rejected")
	}
}
