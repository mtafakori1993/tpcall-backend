package httpapi

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTurnCredentials(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	user, cred := turnCredentials("s3cret", "101", time.Hour, now)

	if want := strconv.FormatInt(now.Add(time.Hour).Unix(), 10) + ":101"; user != want {
		t.Fatalf("username = %q, want %q", user, want)
	}
	mac := hmac.New(sha1.New, []byte("s3cret"))
	mac.Write([]byte(user))
	if want := base64.StdEncoding.EncodeToString(mac.Sum(nil)); cred != want {
		t.Fatalf("credential = %q, want %q", cred, want)
	}
	if strings.Contains(cred, "s3cret") {
		t.Fatal("credential must not contain the shared secret")
	}
}
