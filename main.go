package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/chuhlomin/slack-exporter/pkg/structs"
	"github.com/jessevdk/go-flags"
)

type config struct {
	Channels        string `env:"CHANNELS" long:"channels" description:"Slack channel ID; pass \"public\" to export all public channels"`
	Output          string `env:"OUTPUT" long:"output" description:"Output directory" default:"output"`
	APIToken        string `env:"API_TOKEN" long:"api-token" description:"Slack API Token"`
	AppClientID     string `env:"APP_CLIENT_ID" long:"app-client-id" description:"Slack App Client ID"`
	AppClientSecret string `env:"APP_CLIENT_SECRET" long:"app-client-secret" description:"Slack App Client Secret"`
	Address         string `env:"ADDRESS" long:"address" description:"Server address" default:"localhost"`
	Port            string `env:"PORT" long:"port" description:"Server port" default:"8079"`
	DownloadFiles   bool   `env:"DOWNLOAD_FILES" long:"download-files" description:"Download files"`
	DownloadAvatars bool   `env:"DOWNLOAD_AVATARS" long:"download-avatars" description:"Download avatars"`
	IncludeArchived bool   `env:"SKIP_ARCHIVED" long:"include-archived" description:"Include archived channels"`
	SkipDownloaded  bool   `env:"SKIP_DOWNLOADED" long:"skip-downloaded" description:"Skip already downloaded files"`
}

var (
	cfg                         config
	errBadStatus                = fmt.Errorf("bad status code")
	errExpectedThreeInputs      = fmt.Errorf("expected three inputs")
	errMissingClientIDAndSecret = fmt.Errorf("client ID and secret are required")
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

	if cfg.AppClientID == "" || cfg.AppClientSecret == "" {
		model := initialModelInputs(cfg.AppClientID, cfg.AppClientSecret)
		if _, err := tea.NewProgram(model).Run(); err != nil {
			return fmt.Errorf("could not get inputs: %w", err)
		}

		if len(model.inputs) != 3 {
			return errExpectedThreeInputs
		}

		cfg.AppClientID = model.inputs[0].Value()
		cfg.AppClientSecret = model.inputs[1].Value()
		cfg.APIToken = model.inputs[2].Value()

		if cfg.AppClientID == "" || cfg.AppClientSecret == "" {
			return errMissingClientIDAndSecret
		}
	}

	c := NewSlackClient(cfg.AppClientID, cfg.AppClientSecret)

	if cfg.APIToken == "" {
		err := getToken(c)
		if err != nil {
			return fmt.Errorf("could not get token: %w", err)
		}
	} else {
		c.SetToken(cfg.APIToken)
	}

	// make sure the output directory exists
	if err := os.MkdirAll(cfg.Output, 0o755); err != nil {
		return fmt.Errorf("could not create output directory: %w", err)
	}

	if cfg.Channels == "" {
		model := initialModelChoices(
			cfg.DownloadAvatars,
			cfg.DownloadFiles,
			cfg.IncludeArchived,
			cfg.SkipDownloaded,
		)
		p := tea.NewProgram(model)
		if _, err := p.Run(); err != nil {
			return err
		}

		for i := range model.selected {
			if model.choices[i].isChannel {
				cfg.Channels += model.choices[i].value + ","
			}
			if i == downloadAvatarsIndex {
				cfg.DownloadAvatars = true
			}
			if i == downloadFilesIndex {
				cfg.DownloadFiles = true
			}
			if i == includeArchivedIndex {
				cfg.IncludeArchived = true
			}
			if i == skipDownloadedIndex {
				cfg.SkipDownloaded = true
			}
		}

	} else {
		// support simple aliases for channel types
		switch cfg.Channels {
		case "all":
			cfg.Channels = "public_channel,private_channel,mpim,im"
		case "public":
			cfg.Channels = "public_channel"
		case "private":
			cfg.Channels = "private_channel,mpim,im"
		case "dm":
			cfg.Channels = "im"
		case "group":
			cfg.Channels = "mpim"
		}
	}

	channels := strings.Split(cfg.Channels, ",")

	var channelTypes []string
	for _, channel := range channels {
		switch channel {
		case "public_channel", "private_channel", "mpim", "im":
			channelTypes = append(channelTypes, channel)
		case "":
			continue
		default:
			err := exportChannel(c, channel, cfg.SkipDownloaded)
			if err != nil {
				return fmt.Errorf("could not export channel %q: %w", channel, err)
			}
		}
	}

	if len(channelTypes) > 0 {
		err := exportChannels(c, channelTypes, cfg.SkipDownloaded)
		if err != nil {
			return fmt.Errorf("could not export channels: %w", err)
		}
	}

	if cfg.DownloadAvatars {
		log.Println("Downloading avatars")
		if err := downloadAvatars(c); err != nil {
			return fmt.Errorf("could not download avatars: %w", err)
		}
	}

	return nil
}

func getToken(c *SlackClient) error {
	state := RandStringBytesMaskImprSrcSB(16)
	authorizeURL := c.GetAuthorizeURL(state)

	if err := openBrowser(authorizeURL); err != nil {
		log.Printf("App authorization URL: %s", authorizeURL)
	}

	model := initialModelCode()
	updatedModel, err := tea.NewProgram(model).Run()
	if err != nil {
		return err
	}

	code := strings.TrimSpace(updatedModel.(modelCode).code.Value())
	if err := c.GetToken(code); err != nil {
		return err
	}

	return nil
}

func exportChannel(c *SlackClient, channelID string, skipDownloaded bool) error {
	outputFilename := filepath.Join(cfg.Output, channelID+".json")

	if skipDownloaded {
		if _, err := os.Stat(outputFilename); err == nil {
			return nil
		}
	}

	channelInfo, err := c.GetChannelInfo(channelID)
	if err != nil {
		return fmt.Errorf("could not get channel %q info: %w", channelID, err)
	}

	if channelInfo.IsArchived && !cfg.IncludeArchived {
		return nil
	}

	// check if the file already exists
	if _, err := os.Stat(outputFilename); err == nil {
		// read the file to pull users
		data, err := os.ReadFile(outputFilename)
		if err != nil {
			return fmt.Errorf("could not read file: %w", err)
		}

		var d structs.Data
		if err = json.Unmarshal(data, &d); err != nil {
			return fmt.Errorf("could not unmarshal data: %w", err)
		}

		for id, user := range d.Users {
			c.UsersCache[id] = user
		}
	}

	msgs, err := c.GetMessages(channelID)
	if err != nil {
		return fmt.Errorf("could not get messages: %w", err)
	}

	var files map[string]string
	if cfg.DownloadFiles {
		files, err = c.DownloadFiles(channelID, cfg.SkipDownloaded)
		if err != nil {
			return fmt.Errorf("could not download files: %w", err)
		}
	}

	users, err := c.GetUsers()
	if err != nil {
		return fmt.Errorf("could not get users: %w", err)
	}

	data := structs.Data{
		Channel:  *channelInfo,
		Messages: msgs,
		Users:    users,
		Files:    files,
	}

	// Save to a file
	content, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("could not marshal messages: %w", err)
	}

	if err = os.WriteFile(outputFilename, content, 0o600); err != nil {
		return fmt.Errorf("could not write messages to file: %w", err)
	}

	return nil
}

func exportChannels(c *SlackClient, types []string, skipDownloaded bool) error {
	channels, err := c.GetChannels(types)
	if err != nil {
		return fmt.Errorf("could not get public channels: %w", err)
	}

	prog := progress.New(progress.WithScaledGradient("#FF7CCB", "#FDFF8C"))
	fmt.Print(prog.ViewAs(0))
	previousName := ""

	for i, channel := range channels {
		fmt.Printf(
			"\r%s (%d/%d) %s%s%s",
			prog.ViewAs(float64(i+1)/float64(len(channels))),
			i+1,
			len(channels),
			channel.Name,
			strings.Repeat(" ", max(0, len(previousName)-len(channel.Name))),
			strings.Repeat("\b", max(0, len(previousName)-len(channel.Name))),
		)
		err := exportChannel(c, channel.ID)
		if err != nil {
			return fmt.Errorf("could not export channel %q: %w", channel.Name, err)
		}
		previousName = channel.Name
	}

	fmt.Printf(
		"\r%s (%d/%d) %s%s",
		prog.ViewAs(1),
		len(channels),
		len(channels),
		strings.Repeat(" ", len(previousName)),
		strings.Repeat("\b", len(previousName)),
	)

	return nil
}

func downloadAvatars(c *SlackClient) error {
	err := os.MkdirAll(filepath.Join(cfg.Output, "avatars"), 0o755)
	if err != nil {
		return fmt.Errorf("could not create avatars directory: %w", err)
	}

	for _, user := range c.UsersCache {
		if user.Profile.Image512 != "" {
			err := downloadFile(user.ID, user.Profile.Image512, cfg.Output)
			if err != nil {
				return fmt.Errorf("could not download avatar: %w", err)
			}
		}
	}

	return nil
}

func downloadFile(id, fileURL, output string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fileURL, http.NoBody)
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

	filename := filepath.Join(output, "avatars", id+".png")
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

func openBrowser(someURL string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", someURL)
	case "darwin":
		cmd = exec.Command("open", someURL)
	default:
		cmd = exec.Command("xdg-open", someURL)
	}

	return cmd.Run()
}
