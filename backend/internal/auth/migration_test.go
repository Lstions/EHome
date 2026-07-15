package auth

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestInspectSingleUserMigrationDoesNotWrite(t *testing.T) {
	db := testutil.OpenTestDB(t)
	users := []models.User{
		{Username: "admin", PasswordHash: "hash", Role: "admin", Enabled: true},
		{Username: "viewer", PasswordHash: "hash", Role: "viewer", Enabled: true},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}

	report, err := InspectSingleUserMigration(db)
	if err != nil {
		t.Fatal(err)
	}
	if report.UserCount != 2 || !report.RequiresKeepUserID {
		t.Fatalf("unexpected report: %+v", report)
	}
	for _, item := range report.Users {
		if item.PasswordHash != "" {
			t.Fatal("dry-run report must not expose password hashes")
		}
	}

	var count int64
	if err := db.Model(&models.User{}).Where("subject_key IS NOT NULL OR retired_at IS NOT NULL").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("inspection wrote migration fields for %d users", count)
	}
}

func TestMigrateSingleUserRequiresExplicitKeepIDForMultipleUsers(t *testing.T) {
	db := testutil.OpenTestDB(t)
	users := []models.User{
		{Username: "admin", PasswordHash: "hash", Role: "admin", Enabled: true},
		{Username: "viewer", PasswordHash: "hash", Role: "viewer", Enabled: true},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateSingleUser(db, MigrateOptions{}); err == nil {
		t.Fatal("migration must reject ambiguous multiple-user database")
	}
}

func TestMigrateSingleUserKeepsExplicitSubjectAndRetiresOthers(t *testing.T) {
	db := testutil.OpenTestDB(t)
	users := []models.User{
		{Username: "admin", PasswordHash: "hash", Role: "admin", Enabled: true},
		{Username: "viewer", PasswordHash: "hash", Role: "viewer", Enabled: true},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}

	report, err := MigrateSingleUser(db, MigrateOptions{KeepUserID: users[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if report.KeptUserID != users[0].ID || report.RetiredCount != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}

	var kept, retired models.User
	if err := db.First(&kept, users[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if kept.SubjectKey == nil || *kept.SubjectKey != models.SystemAdminSubjectKey || kept.SessionVersion != 1 {
		t.Fatalf("kept user not normalized: %+v", kept)
	}
	if err := db.First(&retired, users[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if retired.SubjectKey != nil || retired.RetiredAt == nil || retired.Enabled {
		t.Fatalf("other user not retired: %+v", retired)
	}

	state, err := models.LoadAuthState(db)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != models.AuthStateInitialized {
		t.Fatalf("auth state = %q", state.State)
	}
}
