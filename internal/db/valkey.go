package db

import (
	"fmt"

	"github.com/redis/go-redis/v9"
)

func OpenValkey(connectionString string) (*redis.Client, error) {
	options, err := redis.ParseURL(connectionString)
	if err != nil {
		return nil, fmt.Errorf("VALKEY_URL is invalid")
	}
	return redis.NewClient(options), nil
}
