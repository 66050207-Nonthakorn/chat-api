package loadbench

import (
	"errors"
	"time"

	"cloudchat/internal/uuid"
)

// Config contains only workload and public endpoint settings. The room-size
// values are evidence metadata, not a mechanism for changing server fixtures.
type Config struct {
	BaseURL           string
	GroupRoomID       string
	PrivateRoomID     string
	UserAID           string
	UserBID           string
	Connections       int
	MessagesPerSecond int
	Duration          time.Duration
	PayloadBytes      int
	GroupPercent      int
	GroupRoomSize     int
	PrivateRoomSize   int
}

// Validate rejects workload settings outside the API contract and the
// baseline M6 group size.
func (c Config) Validate() error {
	switch {
	case c.BaseURL == "":
		return errors.New("base URL is required")
	case c.Connections < 1:
		return errors.New("connections must be positive")
	case c.MessagesPerSecond < 1:
		return errors.New("messages per second must be positive")
	case c.Duration <= 0:
		return errors.New("duration must be positive")
	case c.PayloadBytes < 1 || c.PayloadBytes > 4096:
		return errors.New("payload bytes must be between 1 and 4096")
	case c.GroupPercent < 0 || c.GroupPercent > 100:
		return errors.New("group percent must be between 0 and 100")
	case c.GroupRoomSize < 1 || c.GroupRoomSize > 20:
		return errors.New("group room size must be between 1 and 20")
	case c.PrivateRoomSize < 2:
		return errors.New("private room size must be at least 2")
	case !uuid.IsCanonical(c.GroupRoomID) || !uuid.IsCanonical(c.PrivateRoomID) || !uuid.IsCanonical(c.UserAID) || !uuid.IsCanonical(c.UserBID):
		return errors.New("room and user IDs must be canonical UUIDs")
	case c.GroupRoomID == c.PrivateRoomID:
		return errors.New("group and private room IDs must differ")
	case c.UserAID == c.UserBID:
		return errors.New("user IDs must differ")
	default:
		return nil
	}
}
