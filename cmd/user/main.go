package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	_ "embed"

	"github.com/jessevdk/go-flags"
	"github.com/slack-go/slack"
)

type config struct {
	Token  string `env:"API_TOKEN" long:"token" description:"Slack API token" required:"true"`
	UserID string `long:"user" short:"u" description:"User ID" required:"true"`
}

var cfg config

func main() {
	if err := run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run() error {
	if _, err := flags.Parse(&cfg); err != nil {
		return fmt.Errorf("could not parse flags: %w", err)
	}

	client := slack.New(cfg.Token)
	user, err := client.GetUserInfo(cfg.UserID)
	if err != nil {
		if err.Error() == "user_not_found" {
			log.Printf("Falling back to user lookup by nickname")

			// try to lookup by nickname
			users, err := client.GetUsers()
			if err != nil {
				return fmt.Errorf("could not get users: %w", err)
			}

			for _, u := range users {
				if u.Profile.DisplayName == cfg.UserID {
					user = &u
					break
				}
			}
		}
		if user == nil {
			return fmt.Errorf("could not get user: %w", err)
		}
	}

	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	if err := enc.Encode(user); err != nil {
		return fmt.Errorf("could not encode user: %w", err)
	}

	fmt.Println(b.String())
	return nil
}
