package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims are the JWT claims issued by tpcall-backend.
type Claims struct {
	Extension string `json:"extension"`
	Name      string `json:"name"`
	jwt.RegisteredClaims
}

var errInvalidToken = errors.New("invalid token")

// SignToken creates a signed JWT for the given user.
func SignToken(secret []byte, userID uuid.UUID, name, extension string, expiration time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		Extension: extension,
		Name:      name,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			Issuer:    "tpcall-backend",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiration)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// ParseToken validates a signed JWT and returns its claims.
func ParseToken(secret []byte, tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errInvalidToken
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errInvalidToken
	}
	if claims.Subject == "" {
		return nil, errInvalidToken
	}
	return claims, nil
}
