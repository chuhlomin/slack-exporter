package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slack-go/slack"
	"golang.org/x/time/rate"

	"github.com/chuhlomin/slack-export/pkg/structs"
)

// TokenResponse represents the response from the Slack API when requesting a token.
// Only Ok and AuthedUser.AccessToken are used.
type TokenResponse struct {
	Ok          bool   `json:"ok"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	BotUserID   string `json:"bot_user_id"`
	AppID       string `json:"app_id"`
	Team        struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	} `json:"team"`
	Enterprise struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	} `json:"enterprise"`
	AuthedUser struct {
		ID          string `json:"id"`
		Scope       string `json:"scope"`
		TokenType   string `json:"token_type"`
		AccessToken string `json:"access_token"`
	} `json:"authed_user"`
}

// SlackClient is a client for the Slack API.
type SlackClient struct {
	limiter      *rate.Limiter
	ctx          context.Context
	clientID     string
	clientSecret string
	token        string
	api          *slack.Client
	seenUsers    map[string]interface{}
	files        map[string]string // id -> url_private_download
}

// NewSlackClient creates a new SlackClient.
func NewSlackClient(id, secret string) *SlackClient {
	return &SlackClient{
		// Tier 3 Rate Limiting: 50 requests per minute
		limiter:      rate.NewLimiter(rate.Every(time.Minute/50), 1),
		ctx:          context.Background(),
		clientID:     id,
		clientSecret: secret,
		seenUsers:    make(map[string]interface{}),
		files:        make(map[string]string),
	}
}

// GetAuthorizeURL returns the URL to authorize the app and start the OAuth flow.
func (sc *SlackClient) GetAuthorizeURL(state string) string {
	url := url.URL{
		Scheme: "https",
		Host:   "slack.com",
		Path:   "/oauth/v2/authorize",
	}

	vals := url.Query()
	vals.Add("scope", "")
	vals.Add("user_scope", strings.Join(
		[]string{
			"channels:history",
			"groups:history",
			"im:history",
			"mpim:history",
			"users:read",
			"channels:read",
			"files:read",
		},
		",",
	))
	vals.Add("redirect_uri", "https://exporter.local")
	vals.Add("client_id", sc.clientID)

	if state != "" {
		vals.Add("state", state)
	}

	url.RawQuery = vals.Encode()

	return url.String()
}

// SetToken sets the API token for the SlackClient.
func (sc *SlackClient) SetToken(token string) {
	sc.token = token
	sc.api = slack.New(token)
}

// GetToken requests a token from the Slack API using the provided code.
func (sc *SlackClient) GetToken(code string) error {
	if code == "" {
		return errors.New("argument 'code' is required")
	}

	// set multipart/form-data values
	multipartData := &bytes.Buffer{}
	writer := multipart.NewWriter(multipartData)
	writer.WriteField("client_id", sc.clientID)
	writer.WriteField("client_secret", sc.clientSecret)
	writer.WriteField("code", code)
	writer.Close()

	req, err := http.NewRequest("POST", "https://slack.com/api/oauth.v2.access", multipartData)
	if err != nil {
		return fmt.Errorf("could not create request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not send request: %v", err)
	}

	defer resp.Body.Close()

	var token TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return fmt.Errorf("could not decode response: %v", err)
	}

	if !token.Ok {
		return fmt.Errorf("error response: %#v", token)
	}

	log.Printf("Token received: %s", token.AuthedUser.AccessToken)

	sc.token = token.AuthedUser.AccessToken
	sc.api = slack.New(sc.token)
	return nil
}

// GetUsers returns a list of users who have posted messages in the channel.
// This method is used to get the user names for the messages.
func (sc *SlackClient) GetUsers() ([]slack.User, error) {
	users := make([]slack.User, 0, len(sc.seenUsers))
	for user := range sc.seenUsers {
		if user == "" {
			continue
		}
		u, err := sc.api.GetUserInfo(user)
		if err != nil {
			return nil, fmt.Errorf("%q: %v", user, err)
		}

		users = append(users, *u)
	}

	return users, nil
}

// GetChannelInfo returns information about the channel, such as the name.
func (sc *SlackClient) GetChannelInfo(channel string) (*slack.Channel, error) {
	if channel == "" {
		return nil, errors.New("argument 'channel' is required")
	}

	return sc.api.GetConversationInfo(&slack.GetConversationInfoInput{ChannelID: channel})
}

// GetMessages returns a list of all the messages in the channel.
func (sc *SlackClient) GetMessages(channel string) ([]structs.Message, error) {
	if channel == "" {
		return nil, errors.New("argument 'channel' is required")
	}

	var allMessages []slack.Message

	cursor := ""
	for {
		err := sc.limiter.Wait(sc.ctx)
		if err != nil {
			return nil, fmt.Errorf("rate limit error: %v", err)
		}

		log.Printf("Getting messages with cursor %q", cursor)

		resp, err := sc.api.GetConversationHistory(&slack.GetConversationHistoryParameters{
			ChannelID: channel,
			Limit:     999,
			Cursor:    cursor,
		})
		if err != nil {
			return nil, err
		}

		allMessages = append(allMessages, resp.Messages...)

		if resp.ResponseMetaData.NextCursor == "" {
			break
		}

		cursor = resp.ResponseMetaData.NextCursor
	}

	var convertedMessages []structs.Message
	for _, msg := range allMessages {
		var replies []slack.Message
		var err error

		if msg.ReplyCount > 0 {
			replies, err = sc.getReplies(channel, msg.Timestamp)
			if err != nil {
				fmt.Printf("Could not get replies for message '%s': %v", msg.Timestamp, err)
			}
		}

		convertedMsg := sc.convertToMsg(msg)
		convertedMsg.Replies = replies
		convertedMessages = append(convertedMessages, convertedMsg)
	}

	return convertedMessages, nil
}

// getReplies returns a list of all the replies to a message.
func (sc *SlackClient) getReplies(channel, messageID string) ([]slack.Message, error) {
	if channel == "" {
		return nil, errors.New("argument 'channel' is required")
	}

	var allReplies []slack.Message

	cursor := ""
	for {
		err := sc.limiter.Wait(sc.ctx)
		if err != nil {
			return nil, fmt.Errorf("rate limit error: %v", err)
		}

		log.Printf("Getting replies with cursor %q for message %q", cursor, messageID)

		msgs, _, nextCursor, err := sc.api.GetConversationReplies(&slack.GetConversationRepliesParameters{
			ChannelID: channel,
			Limit:     999,
			Cursor:    cursor,
			Timestamp: messageID,
		})
		if err != nil {
			return nil, err
		}

		allReplies = append(allReplies, msgs...)

		if nextCursor == "" {
			break
		}

		cursor = nextCursor
	}

	// Filter out reply which matches the parent message
	filterFn := func(replies []slack.Message, parentId string) (ret []slack.Message) {
		for _, r := range replies {
			if r.Timestamp != parentId {
				ret = append(ret, r)
			}
		}
		return
	}
	filteredReplies := filterFn(allReplies, messageID)

	return filteredReplies, nil
}

func (sc *SlackClient) convertToMsg(message slack.Message) structs.Message {
	sc.seenUsers[message.User] = nil

	if message.Files != nil {
		for _, file := range message.Files {
			if file.URLPrivateDownload == "" {
				continue
			}
			sc.files[file.ID] = file.URLPrivateDownload
		}
	}

	return structs.Message{
		Message: message,
	}
}

// DownloadFiles downloads all the files in the channel.
func (sc *SlackClient) DownloadFiles(channelID string) (map[string]string, error) {
	result := make(map[string]string)

	// create directory for files
	err := os.MkdirAll(channelID, 0755)
	if err != nil {
		return nil, fmt.Errorf("could not create directory: %v", err)
	}

	for id, url := range sc.files {
		filename, err := sc.downloadFile(channelID, id, url)
		if err != nil {
			log.Printf("could not download file %q: %v", id, err)
		}

		result[id] = filename
	}

	return result, nil
}

func (sc *SlackClient) downloadFile(path, id, url string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("could not create request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+sc.token)

	err = sc.limiter.Wait(sc.ctx)
	if err != nil {
		return "", fmt.Errorf("rate limit error: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not send request: %v", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bad status code: %d", resp.StatusCode)
	}

	// read content-disposition header
	disposition := resp.Header.Get("Content-Disposition")
	if disposition == "" {
		return "", fmt.Errorf("no content-disposition header")
	}

	// extract filename from content-disposition header
	filename := strings.TrimPrefix(disposition, "attachment; filename=\"")
	// remove everything after ";
	filename = strings.Split(filename, "\";")[0]

	// if filename is empty, use the id
	if filename == "" {
		filename = id
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("could not read body: %v", err)
	}

	// adding id prefix to filename to avoid collisions (like a few files named image.png)
	err = os.WriteFile(filepath.Join(path, id+"-"+filename), content, 0644)
	if err != nil {
		return "", fmt.Errorf("could not write file: %v", err)
	}

	return filename, nil
}
