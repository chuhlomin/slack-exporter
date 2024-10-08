package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "embed"

	"github.com/jessevdk/go-flags"
	"github.com/slack-go/slack"
	"golang.org/x/time/rate"
)

type config struct {
	Token  string `env:"API_TOKEN" long:"token" description:"Slack API token" required:"true"`
	Output string `long:"output" description:"Output directory file" required:"true"`
}

var (
	cfg          config
	errBadStatus = fmt.Errorf("bad status code")
)

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
	emoji, err := client.GetEmoji()
	if err != nil {
		return fmt.Errorf("could not get emoji: %w", err)
	}

	for id, url := range emoji {
		if strings.HasPrefix(url, "alias:") {
			continue
		}

		err := downloadFile(id, url, cfg.Output)
		if err != nil {
			return fmt.Errorf("could not download file: %w", err)
		}
	}

	f, err := os.Create(filepath.Join(cfg.Output, "emoji.json"))
	if err != nil {
		return fmt.Errorf("could not create file: %w", err)
	}

	defer f.Close()

	if err := json.NewEncoder(f).Encode(emoji); err != nil {
		return fmt.Errorf("could not write file %w", err)
	}

	return nil
}

var limiter = rate.NewLimiter(rate.Every(500*time.Millisecond), 1)

func downloadFile(id, fileURL, output string) error {
	ctx := context.Background()
	err := limiter.Wait(ctx)
	if err != nil {
		return fmt.Errorf("could not wait: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, http.NoBody)
	if err != nil {
		return fmt.Errorf("could not create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not send request: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %d", errBadStatus, resp.StatusCode)
	}

	ext := filepath.Ext(fileURL)
	filename := filepath.Join(output, id+ext)
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("could not create file: %w", err)
	}

	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return fmt.Errorf("could not write file: %w", err)
	}

	return nil
}
