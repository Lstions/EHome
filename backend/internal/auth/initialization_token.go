package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

var (
	ErrInvalidInitializationCredential = errors.New("invalid initialization credential")
	ErrInitializationCredentialLocked  = errors.New("initialization credential locked")
)

func CreateInitializationCredential(db *gorm.DB, ttl time.Duration, source string) (string, error) {
	selector, err := randomURLToken(18)
	if err != nil {
		return "", err
	}
	secret, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	token := models.InitializationToken{
		Selector:   selector,
		SecretHash: hashInitializationSecret(secret),
		Source:     source,
		ExpiresAt:  time.Now().UTC().Add(ttl),
	}
	if err := db.Create(&token).Error; err != nil {
		return "", err
	}
	return selector + "." + secret, nil
}

func VerifyInitializationCredential(db *gorm.DB, credential string, maxAttempts int) (models.InitializationToken, error) {
	selector, secret, ok := strings.Cut(credential, ".")
	if !ok || selector == "" || secret == "" {
		return models.InitializationToken{}, ErrInvalidInitializationCredential
	}

	var token models.InitializationToken
	if err := db.Where("selector = ?", selector).First(&token).Error; err != nil {
		return models.InitializationToken{}, ErrInvalidInitializationCredential
	}
	if token.AttemptCount >= maxAttempts {
		return models.InitializationToken{}, ErrInitializationCredentialLocked
	}

	expected := []byte(token.SecretHash)
	actual := []byte(hashInitializationSecret(secret))
	matches := len(expected) == len(actual) && subtle.ConstantTimeCompare(expected, actual) == 1
	validState := token.ConsumedAt == nil && time.Now().UTC().Before(token.ExpiresAt)
	if !matches || !validState {
		result := db.Model(&models.InitializationToken{}).
			Where("id = ? AND attempt_count < ?", token.ID, maxAttempts).
			UpdateColumn("attempt_count", gorm.Expr("attempt_count + 1"))
		if result.Error != nil {
			return models.InitializationToken{}, result.Error
		}
		return models.InitializationToken{}, ErrInvalidInitializationCredential
	}
	return token, nil
}

func hashInitializationSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomURLToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
