package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
)

type DiscordMessageComponent struct {
	Type     int    `json:"type"`
	CustomId string `json:"custom_id"`
	Style    int    `json:"style"`
	Label    string `json:"label"`
}

type DiscordMessageComponents struct {
	Type       int                       `json:"type"`
	Components []DiscordMessageComponent `json:"components"`
}

type DiscordMessageAttachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int    `json:"size"`
	URL      string `json:"url"`
	ProxyURL string `json:"proxy_url"`
	Height   int    `json:"height,omitempty"`
	Width    int    `json:"width,omitempty"`
}

type DiscordMessage struct {
	Id          string                     `json:"id"`
	Type        int                        `json:"type"`
	Timestamp   string                     `json:"timestamp"`
	Content     string                     `json:"content"`
	ChannelId   string                     `json:"channel_id"`
	Attachments []DiscordMessageAttachment `json:"attachments"`
	Author      struct {
		Avatar        string `json:"avatar"`
		Discriminator string `json:"discriminator"`
		ID            string `json:"id"`
		PublicFlags   int    `json:"public_flags"`
		Username      string `json:"username"`
		Flags         int    `json:"flags"`
		AccentColor   int    `json:"accent_color"`
		Bot           bool   `json:"bot"`
		System        bool   `json:"system"`
	}
	Components []DiscordMessageComponents `json:"components"`
}

func MidjourneyLoadChannelMessages(token, channelId string) []DiscordMessage {
	result := LoadBytesFromURL("https://discord.com/api/v9/channels/"+channelId+"/messages?limit=50", token)

	messages := []DiscordMessage{}
	err := json.Unmarshal(result, &messages)
	if err != nil {
		fmt.Println(err)
		return []DiscordMessage{}
	}
	return messages
}

const (
	ApplicationID string = "936929561302675456"
	SessionID     string = "ea8816d857ba9ae2f74c59ae1a953afe"
)

type InteractionsRequest struct {
	Type          int            `json:"type"`
	ApplicationID string         `json:"application_id"`
	MessageFlags  *int           `json:"message_flags,omitempty"`
	MessageID     *string        `json:"message_id,omitempty"`
	ChannelID     string         `json:"channel_id"`
	SessionID     string         `json:"session_id"`
	Data          map[string]any `json:"data"`
}

func MidjourneyCheckResponse(resp *http.Response) error {
	if resp.StatusCode >= 400 {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("Call ioutil.ReadAll failed, err: %w", err)
		}

		return fmt.Errorf("resp.StatusCode: %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

func MidjourneyImagine(token, channelId, prompt string) error {
	interactionsReq := &InteractionsRequest{
		Type:          2,
		ApplicationID: ApplicationID,
		ChannelID:     channelId,
		SessionID:     SessionID,
		Data: map[string]any{
			"version": "1118961510123847772",
			"id":      "938956540159881230",
			"name":    "imagine",
			"type":    "1",
			"options": []map[string]any{
				{
					"type":  3,
					"name":  "prompt",
					"value": prompt,
				},
			},
			"application_command": map[string]any{
				"id":                         "938956540159881230",
				"application_id":             ApplicationID,
				"version":                    "1118961510123847772",
				"default_permission":         true,
				"default_member_permissions": nil,
				"type":                       1,
				"nsfw":                       false,
				"name":                       "imagine",
				"description":                "Create images with Midjourney",
				"dm_permission":              true,
				"options": []map[string]any{
					{
						"type":        3,
						"name":        "prompt",
						"description": "The prompt to imagine",
						"required":    true,
					},
				},
				"attachments": []any{},
			},
		},
	}

	b, _ := json.Marshal(interactionsReq)

	url := "https://discord.com/api/v9/interactions"
	req, err := http.NewRequest("POST", url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("Call http.NewRequest failed, err: %w", err)
	}

	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")

	c := &http.Client{}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("Call c.Do failed, err: %w", err)
	}

	defer resp.Body.Close()

	if err := MidjourneyCheckResponse(resp); err != nil {
		return fmt.Errorf("Call checkResponse failed, err: %w", err)
	}

	return nil
}

func MidjourneyGetImagineResult(token, channelId, prompt, timestamp string) DiscordMessage {
	result := DiscordMessage{}
	startTime, _ := time.Parse(time.RFC3339, timestamp)
	for {
		messages := MidjourneyLoadChannelMessages(token, channelId)
		for _, message := range messages {
			msgTime, _ := time.Parse(time.RFC3339, message.Timestamp)
			if msgTime.Before(startTime) {
				continue
			}
			if strings.HasPrefix(message.Content, "**"+prompt+"**") &&
				!strings.Contains(message.Content, "Image #") {
				if len(message.Attachments) == 0 ||
					strings.Contains(message.Content, "%)") ||
					(len(message.Attachments) > 0 && strings.HasSuffix(message.Attachments[0].URL, ".webp")) {
					break
				} else {
					result = message
					fmt.Println(message)
					break
				}
			}
		}
		if len(result.Attachments) > 0 && result.Attachments[0].URL != "" {
			break
		}
		time.Sleep(1 * time.Second)
	}
	return result
}

func MidjourneyGetUpscaleResult(token, channelId, prompt, label, timestamp string) DiscordMessage {
	textLabel := strings.ReplaceAll(label, "U", "Image #")
	result := DiscordMessage{}
	startTime, _ := time.Parse(time.RFC3339, timestamp)
	for {
		messages := MidjourneyLoadChannelMessages(token, channelId)
		for _, message := range messages {
			msgTime, _ := time.Parse(time.RFC3339, message.Timestamp)
			if msgTime.Before(startTime) {
				continue
			}
			if strings.HasPrefix(message.Content, "**"+prompt+"**") &&
				strings.Contains(message.Content, textLabel) {
				if len(message.Attachments) == 0 ||
					strings.Contains(message.Content, "%)") ||
					(len(message.Attachments) > 0 && strings.HasSuffix(message.Attachments[0].URL, ".webp")) {
					break
				} else {
					result = message
					fmt.Println(message)
					break
				}
			}
		}
		if len(result.Attachments) > 0 && result.Attachments[0].URL != "" {
			break
		}
		time.Sleep(1 * time.Second)
	}
	return result
}

func LoadBytesFromURL(url string, token string) []byte {
	// Create a new request using http
	req, err := http.NewRequest("GET", url, nil)

	// add authorization header to the req
	req.Header.Add("Authorization", token)

	// Send req using http Client
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Error on response.\n[ERRO] -", err)
		return []byte{}
	}

	body, _ := ioutil.ReadAll(resp.Body)
	return []byte(body)
}

type UpscaleRequest struct {
	Index     int32  `json:"index"`
	ChannelID string `json:"channel_id"`
}

func MidjourneyUpscale(token, channelId, prompt string, message DiscordMessage, label string) error {
	upscaleComponent := DiscordMessageComponent{}
	for _, components := range message.Components {
		for _, component := range components.Components {
			if component.Label == label {
				upscaleComponent = component
				break
			}
		}
	}
	if upscaleComponent.CustomId == "" {
		return fmt.Errorf("upscaleComponent.CustomId is empty")
	}
	flags := 0
	interactionsReq := &InteractionsRequest{
		Type:          3,
		ApplicationID: ApplicationID,
		ChannelID:     message.ChannelId,
		MessageFlags:  &flags,
		MessageID:     &message.Id,
		SessionID:     SessionID,
		Data: map[string]any{
			"component_type": 2,
			"custom_id":      upscaleComponent.CustomId,
		},
	}

	b, _ := json.Marshal(interactionsReq)

	url := "https://discord.com/api/v9/interactions"
	req, err := http.NewRequest("POST", url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("Call http.NewRequest failed, err: %w", err)
	}

	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")

	c := &http.Client{}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("Call c.Do failed, err: %w", err)
	}

	defer resp.Body.Close()

	if err := MidjourneyCheckResponse(resp); err != nil {
		return fmt.Errorf("Call checkResponse failed, err: %w", err)
	}

	return nil
}
