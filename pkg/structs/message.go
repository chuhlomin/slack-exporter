// Package structs contains the data structures shared between
// the main app that exports Slack messages and the JSON to HTML converter.
package structs

import (
	"errors"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

const sameContextDuration = 15 * time.Minute

var errNoDotInTimestamp = errors.New("no dot in timestamp")

// Message is a wrapper for slack.Message with replies.
type Message struct {
	slack.Message
	Replies []slack.Message `json:"replies,omitempty"`
}

func (m *Message) SameContext(m2 Message) bool {
	if m.User != m2.User {
		return false
	}

	mSec, err := extractUnixTimestamp(m.Timestamp)
	if err != nil {
		log.Printf("could not extract timestamp from %q: %v", m.Timestamp, err)
		return false
	}

	m2Sec, err := extractUnixTimestamp(m2.Timestamp)
	if err != nil {
		log.Printf("could not extract timestamp from %q: %v", m2.Timestamp, err)
		return false
	}

	if time.Duration(math.Abs(float64(mSec-m2Sec))) > sameContextDuration {
		return false
	}

	return true
}

func extractUnixTimestamp(ts string) (int64, error) {
	dotIndex := strings.Index(ts, ".")
	if dotIndex == -1 {
		return 0, errNoDotInTimestamp
	}

	ts = ts[:dotIndex]
	return strconv.ParseInt(ts, 10, 64)
}

// Data struct used to marshal/unmarshal JSON data.
type Data struct {
	Channel  slack.Channel          `json:"channel"`
	Messages []Message              `json:"messages"`
	Users    map[string]*slack.User `json:"users"`
	Files    map[string]string      `json:"files"`
}

func GetChannelName(sc slack.Channel) string {
	if sc.Name != "" {
		return sc.Name
	}
	if sc.IsGeneral {
		return "general"
	}
	if sc.IsIM {
		return sc.User
	}
	return sc.ID
}
