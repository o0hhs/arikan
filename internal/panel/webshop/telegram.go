package webshop

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"
)

// tgLogin is the Telegram Login Widget's payload. The hash is checked with the bot
// token; the token never reaches the browser.
type tgLogin struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	PhotoURL  string `json:"photo_url"`
	AuthDate  int64  `json:"auth_date"`
	Hash      string `json:"hash"`
}

func (t tgLogin) valid(token string, now time.Time) bool {
	if t.ID == 0 || t.Hash == "" || token == "" || t.AuthDate == 0 {
		return false
	}
	if now.Unix()-t.AuthDate > int64((24*time.Hour)/time.Second) || t.AuthDate > now.Add(time.Minute).Unix() {
		return false
	}
	pairs := []string{"auth_date=" + strconv.FormatInt(t.AuthDate, 10), "first_name=" + t.FirstName, "id=" + strconv.FormatInt(t.ID, 10)}
	if t.LastName != "" {
		pairs = append(pairs, "last_name="+t.LastName)
	}
	if t.PhotoURL != "" {
		pairs = append(pairs, "photo_url="+t.PhotoURL)
	}
	if t.Username != "" {
		pairs = append(pairs, "username="+t.Username)
	}
	sort.Strings(pairs)
	secret := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(pairs, "\n")))
	want, err := hex.DecodeString(t.Hash)
	if err != nil {
		return false
	}
	return hmac.Equal(mac.Sum(nil), want)
}
