package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"ehome/backend/internal/models"

	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

const (
	TokenIssuer   = "ehome"
	TokenAudience = "ehome-api"
)

var ErrInvalidSession = errors.New("invalid session")

type SessionClaims struct {
	SessionVersion int64 `json:"sv"`
	jwt.RegisteredClaims
}

func SignSessionToken(user models.User, secret []byte, ttl time.Duration) (string, error) {
	if user.ID == 0 || user.SessionVersion <= 0 {
		return "", ErrInvalidSession
	}
	jti, err := randomJTI()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	claims := SessionClaims{
		SessionVersion: user.SessionVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    TokenIssuer,
			Subject:   strconv.FormatUint(uint64(user.ID), 10),
			Audience:  jwt.ClaimStrings{TokenAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        jti,
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func ParseSessionToken(raw string, secret []byte, now time.Time) (*SessionClaims, error) {
	claims := &SessionClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, ErrInvalidSession
		}
		return secret, nil
	},
		jwt.WithIssuer(TokenIssuer),
		jwt.WithAudience(TokenAudience),
		jwt.WithLeeway(time.Minute),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil || !token.Valid || claims.Subject == "" || claims.ID == "" || claims.IssuedAt == nil || claims.ExpiresAt == nil || claims.SessionVersion <= 0 {
		return nil, ErrInvalidSession
	}
	if _, err := strconv.ParseUint(claims.Subject, 10, 64); err != nil {
		return nil, ErrInvalidSession
	}
	return claims, nil
}

func ValidateSessionClaims(db *gorm.DB, claims *SessionClaims) (models.User, error) {
	if claims == nil || claims.SessionVersion <= 0 {
		return models.User{}, ErrInvalidSession
	}
	userID, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil {
		return models.User{}, ErrInvalidSession
	}
	state, err := models.LoadAuthState(db)
	if err != nil || state.State != models.AuthStateInitialized {
		return models.User{}, ErrInvalidSession
	}
	var user models.User
	err = db.Where("id = ? AND subject_key = ? AND retired_at IS NULL AND enabled = ?", userID, models.SystemAdminSubjectKey, true).First(&user).Error
	if err != nil {
		return models.User{}, ErrInvalidSession
	}
	if user.SessionVersion != claims.SessionVersion {
		return models.User{}, ErrInvalidSession
	}
	return user, nil
}

func randomJTI() (string, error) {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate jti: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
