package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/option"
	"gopkg.in/yaml.v2"

	gpt3 "chat_bot/gpt3"

	translate "cloud.google.com/go/translate/apiv3"
	"cloud.google.com/go/translate/apiv3/translatepb"
	openai "github.com/0x9ef/openai-go"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	tokenizer "github.com/samber/go-gpt-3-encoder"
)

const (
	GPT4Model          = "gpt-4"
	GPT4Model0613      = "gpt-4-0613"
	GPT4Model1106      = "gpt-4-1106-preview"
	GPT35TurboModel    = "gpt-3.5-turbo-0613"
	GPT35TurboModel16k = "gpt-3.5-turbo-16k"
	BardModel          = "bard"
	DalleModel         = "dall-e-3"
	MidjourneyModel    = "midjourney"
)

const DefaultModel = GPT4Model1106
const DefaultSystemPrompt = "You are a helpful AI assistant."

var config Config

// Store conversation history per user
var conversationHistory = make(map[int64][]gpt3.ChatCompletionRequestMessage)
var userSettingsMap = make(map[int64]User)
var mu = &sync.Mutex{}

type User struct {
	Model                string
	SystemPrompt         string
	State                string
	CurrentContext       *context.CancelFunc
	CurrentMessageBuffer string
	BardChatbot          *BardChatbot
}

type Config struct {
	DebugMode                        string   `yaml:"debug_mode"`
	TelegramToken                    string   `yaml:"telegram_token"`
	OpenAIKey                        string   `yaml:"openai_api_key"`
	BardSession                      string   `yaml:"bard_session_id"`
	AllowedUsers                     []string `yaml:"allowed_telegram_usernames"`
	BardAllowedUsers                 []string `yaml:"bard_allowed_telegram_usernames"`
	MidjourneyToken                  string   `yaml:"midjourney_token"`
	MidjourneyChannelId              string   `yaml:"midjourney_channel_id"`
	MidjourneyTranslateRUENUsernames []string `yaml:"midjourney_translate_ru_en_usernames"`
	GoogleCloudProjectName           string   `yaml:"google_cloud_project_name"`
	GoogleCloudKeyfile               string   `yaml:"google_cloud_keyfile"`
}

func ReadConfig() (Config, error) {
	var config Config
	configFile, err := os.Open("config.yml")
	if err != nil {
		return config, err
	}
	defer configFile.Close()
	decoder := yaml.NewDecoder(configFile)
	err = decoder.Decode(&config)
	if err != nil {
		return config, err
	}
	return config, nil
}

const (
	StateDefault                = ""
	StateWaitingForSystemPrompt = "waiting_for_system_prompt"
)

// var openaiClientGPT4 gpt3.Client
var openaiClient gpt3.Client

func substr(input string, start int, length int) string {
	asRunes := []rune(input)

	if start >= len(asRunes) {
		return ""
	}

	if start+length > len(asRunes) {
		length = len(asRunes) - start
	}

	return string(asRunes[start : start+length])
}

func translateText(projectID string, sourceLang string, targetLang string, text string) ([]string, error) {
	translations := []string{}

	tempdir, e := ioutil.TempDir("", "chatbot")
	if e != nil {
		log.Fatal(e)
	}
	accountKeyPath := tempdir + "/gckey.json"
	defer os.RemoveAll(tempdir)
	ioutil.WriteFile(accountKeyPath, []byte(config.GoogleCloudKeyfile), 0644)

	// Instantiates a client
	ctx := context.Background()
	client, err := translate.NewTranslationClient(ctx, option.WithCredentialsFile(accountKeyPath))
	if err != nil {
		return translations, fmt.Errorf("NewTranslationClient: %w", err)
	}
	defer client.Close()

	// Construct request
	req := &translatepb.TranslateTextRequest{
		Parent:             fmt.Sprintf("projects/%s/locations/global", projectID),
		SourceLanguageCode: sourceLang,
		TargetLanguageCode: targetLang,
		MimeType:           "text/plain", // Mime types: "text/plain", "text/html"
		Contents:           []string{text},
	}

	resp, err := client.TranslateText(ctx, req)
	if err != nil {
		return translations, fmt.Errorf("TranslateText: %w", err)
	}

	// Display the translation for each input text provided
	for _, translation := range resp.GetTranslations() {
		translations = append(translations, translation.GetTranslatedText())
		fmt.Printf("Translated text: %v\n", translation.GetTranslatedText())
	}

	return translations, nil
}

func detectLanguage(projectID string, text string) (string, error) {
	tempdir, e := ioutil.TempDir("", "chatbot")
	if e != nil {
		log.Fatal(e)
	}
	accountKeyPath := tempdir + "/gckey.json"
	defer os.RemoveAll(tempdir)
	ioutil.WriteFile(accountKeyPath, []byte(config.GoogleCloudKeyfile), 0644)

	// Instantiates a client
	ctx := context.Background()
	client, err := translate.NewTranslationClient(ctx, option.WithCredentialsFile(accountKeyPath))
	if err != nil {
		return "", fmt.Errorf("NewTranslationClient: %w", err)
	}
	defer client.Close()

	req := &translatepb.DetectLanguageRequest{
		Parent:   fmt.Sprintf("projects/%s/locations/global", projectID),
		MimeType: "text/plain", // Mime types: "text/plain", "text/html"
		Source: &translatepb.DetectLanguageRequest_Content{
			Content: text,
		},
	}

	resp, err := client.DetectLanguage(ctx, req)
	if err != nil {
		return "", fmt.Errorf("DetectLanguage: %w", err)
	}

	// Display list of detected languages sorted by detection confidence.
	// The most probable language is first.
	languages := resp.GetLanguages()
	for _, language := range languages {
		// The language detected.
		fmt.Printf("Language code: %v\n", language.GetLanguageCode())
		// Confidence of detection result for this language.
		fmt.Printf("Confidence: %v\n", language.GetConfidence())
	}

	language := ""
	if len(languages) > 0 {
		language = languages[0].GetLanguageCode()
	}

	return language, nil
}

func main() {

	var err error
	config, err = ReadConfig()
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
	}
	// Initialize the OpenAI API client
	//customOpenAIAPIEndpoint := os.Getenv("CUSTOM_OPENAI_API_ENDPOINT")
	//openaiClientGPT4 = gpt3.NewClient(config.OpenAIKey, gpt3.WithBaseURL(customOpenAIAPIEndpoint+"/v1"))
	openaiClient = gpt3.NewClient(config.OpenAIKey)

	// Initialize the Telegram bot
	bot, err := tgbotapi.NewBotAPI(config.TelegramToken)
	if err != nil {
		log.Fatalf("Failed to create Telegram bot: %v", err)
	}

	if config.DebugMode == "1" {
		bot.Debug = true
	}
	log.Printf("Authorized on account %s", bot.Self.UserName)

	// Listen for updates
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)
	if err != nil {
		log.Fatalf("Failed to get updates channel: %v", err)
	}

	// Handle updates
	for update := range updates {
		go func(update tgbotapi.Update) {
			if update.Message == nil && update.CallbackQuery == nil {
				return
			}
			if update.Message != nil {
				if update.Message.IsCommand() {
					handleCommand(bot, update)
				} else {
					handleMessage(bot, update)
				}
			} else {
				handleMessage(bot, update)
			}
		}(update)
	}
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func convertOGAToMP3(ogaData []byte) ([]byte, error) {
	if len(ogaData) == 0 {
		return nil, errors.New("OGA data is empty")
	}

	// Prepare FFMPEG command to convert OGA to MP3
	cmd := exec.Command("ffmpeg", "-i", "pipe:0", "-f", "mp3", "pipe:1")
	cmd.Stdin = bytes.NewReader(ogaData)

	// Execute the command and read the output
	var mp3Data bytes.Buffer
	cmd.Stdout = &mp3Data
	err := cmd.Run()
	if err != nil {
		return nil, err
	}

	return mp3Data.Bytes(), nil
}

func convertAudioToText(message *tgbotapi.Message, bot *tgbotapi.BotAPI) string {
	fileId := ""
	if message.Voice != nil {
		fileId = message.Voice.FileID
	} else if message.Audio != nil {
		fileId = message.Audio.FileID
	}
	// Download audio file
	fileURL, err := bot.GetFileDirectURL(fileId)
	if err != nil {
		log.Println(err)
		return ""
	}

	if filepath.Ext(fileURL) != ".oga" {
		fmt.Println("Unsupported audio format: " + filepath.Ext(fileURL))
		return ""
	}

	resp, err := http.Get(fileURL)
	if err != nil {
		log.Println(err)
		return ""
	}
	defer resp.Body.Close()

	// Decode the audio file from base64 encoding
	ogaAudioBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		// TODO: Handle error
		return ""
	}

	mp3AudioBytes, err := convertOGAToMP3(ogaAudioBytes)
	if err != nil {
		return ""
	}

	audioOpts := &openai.AudioOptions{
		File:        bytes.NewBuffer(mp3AudioBytes),
		AudioFormat: "mp3",
		Model:       openai.ModelWhisper,
		Temperature: 0,
	}
	oai := openai.New(config.OpenAIKey)
	r, err := oai.Transcribe(context.Background(), &openai.TranscribeOptions{AudioOptions: audioOpts})
	if err != nil {
		return ""
	}
	return r.Text
}

func telegramPrepareMarkdownMessageV1(msg string) string {
	result := msg

	entities := []string{"_"}

	for _, entity := range entities {
		result = strings.ReplaceAll(result, entity, `\`+entity)
	}
	return result
}

func telegramPrepareMarkdownMessageV2(msg string) string {
	result := msg

	entities := []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}

	for _, entity := range entities {
		result = strings.ReplaceAll(result, entity, `\`+entity)
	}
	if strings.Count(result, "```")%2 == 1 {
		result += "\n```"
	}
	return result
}

func handleMessage(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	state := ""
	model := ""
	chatId := int64(0)
	if update.Message != nil {
		chatId = update.Message.Chat.ID
		if !contains(config.AllowedUsers, update.Message.From.UserName) {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Вам нельзя пользоваться этим ботом")
			bot.Send(msg)
			return
		}
		mu.Lock()
		state = userSettingsMap[update.Message.Chat.ID].State
		model = userSettingsMap[update.Message.Chat.ID].Model
		if model == "" {
			model = DefaultModel
		}
		mu.Unlock()
		if state == StateWaitingForSystemPrompt {
			mu.Lock()
			userSettingsMap[update.Message.Chat.ID] = User{
				Model:        model,
				SystemPrompt: update.Message.Text,
				State:        StateDefault,
			}
			mu.Unlock()
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Системный промпт установлен.")
			bot.Send(msg)
			return
		}
	}
	/*generatedText, err := generateTextWithGPT(update.Message.Text, update.Message.Chat.ID, model)
	if err != nil {
		log.Printf("Failed to generate text with GPT: %v", err)
		return
	}

	msg := tgbotapi.NewMessage(update.Message.Chat.ID, generatedText)
	msg.ReplyToMessageID = update.Message.MessageID

	_, err = bot.Send(msg)
	if err != nil {
		log.Printf("Failed to send message: %v", err)
	}*/
	messageText := ""
	inputPhotoUrl := ""
	if update.Message != nil {
		messageText = update.Message.Text
		if update.Message.Voice != nil || update.Message.Audio != nil {
			messageText = convertAudioToText(update.Message, bot)
			if messageText == "" {
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Не удалось распознать аудио")
				msg.ReplyToMessageID = update.Message.MessageID
				_, err := bot.Send(msg)
				if err != nil {
					fmt.Println(err)
				}
			}

			msg := tgbotapi.NewMessage(update.Message.Chat.ID, messageText)
			msg.DisableWebPagePreview = true
			_, err := bot.Send(msg)
			if err != nil {
				log.Printf("Failed to send message: %v", err)
			}
		}
		if update.Message.Photo != nil {
			messageText = update.Message.Caption
			inputPhotoUrl, _ = bot.GetFileDirectURL(update.Message.Photo[len(update.Message.Photo)-1].FileID)
			time.Sleep(5 * time.Second)
		}
	}
	type MidjourneyCommandMessage struct {
		Id      string `json:"i"`
		Command string `json:"m"`
	}
	midjourneyMessageInfo := MidjourneyCommandMessage{}

	if update.CallbackQuery != nil {
		json.Unmarshal([]byte(update.CallbackQuery.Data), &midjourneyMessageInfo)
		messageText = midjourneyMessageInfo.Command
		chatId = update.CallbackQuery.Message.Chat.ID

		callback := tgbotapi.NewCallback(update.CallbackQuery.ID, "Выполняю запрос...")
		if _, err := bot.Request(callback); err != nil {
			panic(err)
		}
	}
	if messageText != "" {
		if userSettingsMap[chatId].Model == BardModel {
			response, err := userSettingsMap[chatId].BardChatbot.Ask(messageText)
			if err != nil {
				msg := tgbotapi.NewMessage(chatId, "Ошибка обращаения к Bard: "+err.Error())
				msg.ReplyToMessageID = update.Message.MessageID
				msg.DisableWebPagePreview = true
				_, err := bot.Send(msg)
				if err != nil {
					fmt.Println(err)
				}
			} else {
				response = userSettingsMap[chatId].BardChatbot.PrepareForTelegramMarkdown(response)
				msg := tgbotapi.NewMessage(chatId, response)
				msg.ParseMode = "Markdown"
				msg.ReplyToMessageID = update.Message.MessageID
				_, err := bot.Send(msg)
				if err != nil {
					log.Printf("Failed to send message as Markdown: %v"+response, err)
					msg := tgbotapi.NewMessage(chatId, response)
					msg.ReplyToMessageID = update.Message.MessageID
					msg.DisableWebPagePreview = true
					_, err := bot.Send(msg)
					if err != nil {
						log.Printf("Failed to send message: %v", err)
					}
				}
			}
		} else if userSettingsMap[chatId].Model == DalleModel {
			conversationHistory[chatId] = []gpt3.ChatCompletionRequestMessage{
				{
					Role:    "user",
					Content: messageText,
				},
			}
			result := DalleGenerations(config.OpenAIKey, messageText, 1, "1024x1024")
			if len(result.Data) == 0 {
				msg := tgbotapi.NewMessage(chatId, "Ошибка при отправке запроса к OpenAI: "+fmt.Sprint(result))
				msg.ReplyToMessageID = update.Message.MessageID
				msg.DisableWebPagePreview = true
				_, err := bot.Send(msg)
				if err != nil {
					log.Printf("Failed to send message: %v", err)
				}
			} else {
				photoBytes := LoadBytesFromURL(result.Data[0].Url, "")
				photoFileBytes := tgbotapi.FileBytes{
					Name:  "picture",
					Bytes: photoBytes,
				}
				msg := tgbotapi.NewPhoto(chatId, photoFileBytes)
				if update.Message != nil {
					msg.ReplyToMessageID = update.Message.MessageID
				}
				_, err := bot.Send(msg)
				if err != nil {
					log.Printf("Failed to send message: %v", err)
				}
			}
		} else if userSettingsMap[chatId].Model == MidjourneyModel {
			//startTime := time.Now().UTC().Add(-time.Hour * 24 * 7)
			startTime := time.Now().UTC()
			startTimeTimestamp := startTime.Format(time.RFC3339)

			if messageText == "U1" || messageText == "U2" || messageText == "U3" || messageText == "U4" ||
				messageText == "V1" || messageText == "V2" || messageText == "V3" || messageText == "V4" {
				label := messageText
				midjourneyMessage := MidjourneyLoadChannelMessage(config.MidjourneyToken, config.MidjourneyChannelId, midjourneyMessageInfo.Id)
				midjourneyMessagePrompt := ""
				if strings.Contains(midjourneyMessage.Content, "**") {
					midjourneyMessagePrompt = substr(midjourneyMessage.Content, strings.Index(midjourneyMessage.Content, "**")+2, strings.Index(midjourneyMessage.Content[strings.Index(midjourneyMessage.Content, "**")+2:], "**"))
				}
				err := MidjourneyUpscaleOrVariation(config.MidjourneyToken, config.MidjourneyChannelId, midjourneyMessage, label)
				if err != nil {
					msg := tgbotapi.NewMessage(chatId, "Ошибка при отправке запроса к Midjourney: "+fmt.Sprint(err))
					msg.ReplyToMessageID = update.CallbackQuery.Message.MessageID
					msg.DisableWebPagePreview = true
					_, err := bot.Send(msg)
					if err != nil {
						log.Printf("Failed to send message: %v", err)
					}
				}
				replyMessage := tgbotapi.Message{}
				replyMessageCreated := false
				for {
					result, finalResult := MidjourneyGetUpscaleOrVariationResult(config.MidjourneyToken, config.MidjourneyChannelId, midjourneyMessagePrompt, label, startTimeTimestamp)
					if len(result.Attachments) == 0 || result.Attachments[0].URL == "" {
						msg := tgbotapi.NewMessage(chatId, "Ошибка при отправке запроса к Midjourney")
						msg.ReplyToMessageID = update.CallbackQuery.Message.MessageID
						msg.DisableWebPagePreview = true
						_, err := bot.Send(msg)
						if err != nil {
							log.Printf("Failed to send message: %v", err)
						}
					} else {
						photoBytes := LoadBytesFromURL(result.Attachments[0].URL, "")
						photoFileBytes := tgbotapi.FileBytes{
							Name:  "picture",
							Bytes: photoBytes,
						}
						var photoOptions = tgbotapi.InlineKeyboardMarkup{}

						switch messageText {
						case "U1":
							fallthrough
						case "U2":
							fallthrough
						case "U3":
							fallthrough
						case "U4":
							midjourneyMessageInfo := MidjourneyCommandMessage{
								Id:      result.Id,
								Command: "ZO20",
							}
							midjourneyMessageInfoS1, _ := json.Marshal(midjourneyMessageInfo)
							midjourneyMessageInfo = MidjourneyCommandMessage{
								Id:      result.Id,
								Command: "ZO15",
							}
							midjourneyMessageInfoS2, _ := json.Marshal(midjourneyMessageInfo)
							photoOptions = tgbotapi.NewInlineKeyboardMarkup(
								tgbotapi.NewInlineKeyboardRow(
									tgbotapi.NewInlineKeyboardButtonData("Расширить 2x", string(midjourneyMessageInfoS1)),
									tgbotapi.NewInlineKeyboardButtonData("Расширить 1.5x", string(midjourneyMessageInfoS2)),
								),
							)
						case "V1":
							fallthrough
						case "V2":
							fallthrough
						case "V3":
							fallthrough
						case "V4":
							midjourneyMessageInfo := MidjourneyCommandMessage{
								Id:      result.Id,
								Command: "U1",
							}
							midjourneyMessageInfoS1, _ := json.Marshal(midjourneyMessageInfo)
							midjourneyMessageInfo = MidjourneyCommandMessage{
								Id:      result.Id,
								Command: "U2",
							}
							midjourneyMessageInfoS2, _ := json.Marshal(midjourneyMessageInfo)
							midjourneyMessageInfo = MidjourneyCommandMessage{
								Id:      result.Id,
								Command: "U3",
							}
							midjourneyMessageInfoS3, _ := json.Marshal(midjourneyMessageInfo)
							midjourneyMessageInfo = MidjourneyCommandMessage{
								Id:      result.Id,
								Command: "U4",
							}
							midjourneyMessageInfoS4, _ := json.Marshal(midjourneyMessageInfo)
							midjourneyMessageInfo = MidjourneyCommandMessage{
								Id:      result.Id,
								Command: "V1",
							}
							midjourneyMessageInfoS5, _ := json.Marshal(midjourneyMessageInfo)
							midjourneyMessageInfo = MidjourneyCommandMessage{
								Id:      result.Id,
								Command: "V2",
							}
							midjourneyMessageInfoS6, _ := json.Marshal(midjourneyMessageInfo)
							midjourneyMessageInfo = MidjourneyCommandMessage{
								Id:      result.Id,
								Command: "V3",
							}
							midjourneyMessageInfoS7, _ := json.Marshal(midjourneyMessageInfo)
							midjourneyMessageInfo = MidjourneyCommandMessage{
								Id:      result.Id,
								Command: "V4",
							}
							midjourneyMessageInfoS8, _ := json.Marshal(midjourneyMessageInfo)

							photoOptions = tgbotapi.NewInlineKeyboardMarkup(
								tgbotapi.NewInlineKeyboardRow(
									tgbotapi.NewInlineKeyboardButtonData("Увеличить 1", string(midjourneyMessageInfoS1)),
									tgbotapi.NewInlineKeyboardButtonData("Увеличить 2", string(midjourneyMessageInfoS2)),
								),
								tgbotapi.NewInlineKeyboardRow(
									tgbotapi.NewInlineKeyboardButtonData("Увеличить 3", string(midjourneyMessageInfoS3)),
									tgbotapi.NewInlineKeyboardButtonData("Увеличить 4", string(midjourneyMessageInfoS4)),
								),
								tgbotapi.NewInlineKeyboardRow(
									tgbotapi.NewInlineKeyboardButtonData("Вариация 1", string(midjourneyMessageInfoS5)),
									tgbotapi.NewInlineKeyboardButtonData("Вариация 2", string(midjourneyMessageInfoS6)),
								),
								tgbotapi.NewInlineKeyboardRow(
									tgbotapi.NewInlineKeyboardButtonData("Вариация 3", string(midjourneyMessageInfoS7)),
									tgbotapi.NewInlineKeyboardButtonData("Вариация 4", string(midjourneyMessageInfoS8)),
								),
							)
						}

						if replyMessageCreated {
							msg := tgbotapi.EditMessageMediaConfig{
								BaseEdit: tgbotapi.BaseEdit{
									ChatID:    chatId,
									MessageID: replyMessage.MessageID,
								},
								Media: tgbotapi.NewInputMediaPhoto(photoFileBytes),
							}
							bot.Send(msg)

							if finalResult {
								msg := tgbotapi.NewEditMessageReplyMarkup(chatId, replyMessage.MessageID, photoOptions)
								bot.Send(msg)
							}
						} else {
							msg := tgbotapi.NewPhoto(chatId, photoFileBytes)
							msg.ReplyToMessageID = update.CallbackQuery.Message.MessageID
							captions := make(map[string]string)
							captions["U1"] = "Увеличено 1"
							captions["U2"] = "Увеличено 2"
							captions["U3"] = "Увеличено 3"
							captions["U4"] = "Увеличено 4"
							captions["V1"] = "Вариация 1"
							captions["V2"] = "Вариация 2"
							captions["V3"] = "Вариация 3"
							captions["V4"] = "Вариация 4"
							msg.Caption = captions[label]
							if finalResult {
								msg.ReplyMarkup = photoOptions
							}
							replyMessage, err = bot.Send(msg)
							if err != nil {
								log.Printf("Failed to send message: %v", err)
							}
							replyMessageCreated = true
							if !finalResult {
								time.Sleep(2 * time.Second)
							}
						}
						if finalResult {
							break
						}
					}
				}
			} else if messageText == "ZO20" || messageText == "ZO15" {
				label := ""
				switch messageText {
				case "ZO20":
					label = "Zoom Out"
				case "ZO15":
					label = "Zoom Out"
				}
				midjourneyMessage := MidjourneyLoadChannelMessage(config.MidjourneyToken, config.MidjourneyChannelId, midjourneyMessageInfo.Id)
				midjourneyMessagePrompt := ""
				if strings.Contains(midjourneyMessage.Content, "**") {
					midjourneyMessagePrompt = substr(midjourneyMessage.Content, strings.Index(midjourneyMessage.Content, "**")+2, strings.Index(midjourneyMessage.Content[strings.Index(midjourneyMessage.Content, "**")+2:], "**"))
				}
				err := MidjourneyOutpaint(config.MidjourneyToken, config.MidjourneyChannelId, midjourneyMessage, messageText)
				if err != nil {
					msg := tgbotapi.NewMessage(chatId, "Ошибка при отправке запроса к Midjourney: "+fmt.Sprint(err))
					msg.ReplyToMessageID = update.CallbackQuery.Message.MessageID
					msg.DisableWebPagePreview = true
					_, err := bot.Send(msg)
					if err != nil {
						log.Printf("Failed to send message: %v", err)
					}
				}
				replyMessage := tgbotapi.Message{}
				replyMessageCreated := false
				for {
					result, finalResult := MidjourneyGetOutpaintResult(config.MidjourneyToken, config.MidjourneyChannelId, midjourneyMessagePrompt, label, startTimeTimestamp)
					if len(result.Attachments) == 0 || result.Attachments[0].URL == "" {
						msg := tgbotapi.NewMessage(chatId, "Ошибка при отправке запроса к Midjourney")
						msg.ReplyToMessageID = update.CallbackQuery.Message.MessageID
						msg.DisableWebPagePreview = true
						_, err := bot.Send(msg)
						if err != nil {
							log.Printf("Failed to send message: %v", err)
						}
					} else {
						photoBytes := LoadBytesFromURL(result.Attachments[0].URL, "")
						photoFileBytes := tgbotapi.FileBytes{
							Name:  "picture",
							Bytes: photoBytes,
						}
						midjourneyMessageInfo := MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "U1",
						}
						midjourneyMessageInfoS1, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "U2",
						}
						midjourneyMessageInfoS2, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "U3",
						}
						midjourneyMessageInfoS3, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "U4",
						}
						midjourneyMessageInfoS4, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "V1",
						}
						midjourneyMessageInfoS5, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "V2",
						}
						midjourneyMessageInfoS6, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "V3",
						}
						midjourneyMessageInfoS7, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "V4",
						}
						midjourneyMessageInfoS8, _ := json.Marshal(midjourneyMessageInfo)

						var photoOptions = tgbotapi.NewInlineKeyboardMarkup(
							tgbotapi.NewInlineKeyboardRow(
								tgbotapi.NewInlineKeyboardButtonData("Увеличить 1", string(midjourneyMessageInfoS1)),
								tgbotapi.NewInlineKeyboardButtonData("Увеличить 2", string(midjourneyMessageInfoS2)),
							),
							tgbotapi.NewInlineKeyboardRow(
								tgbotapi.NewInlineKeyboardButtonData("Увеличить 3", string(midjourneyMessageInfoS3)),
								tgbotapi.NewInlineKeyboardButtonData("Увеличить 4", string(midjourneyMessageInfoS4)),
							),
							tgbotapi.NewInlineKeyboardRow(
								tgbotapi.NewInlineKeyboardButtonData("Вариация 1", string(midjourneyMessageInfoS5)),
								tgbotapi.NewInlineKeyboardButtonData("Вариация 2", string(midjourneyMessageInfoS6)),
							),
							tgbotapi.NewInlineKeyboardRow(
								tgbotapi.NewInlineKeyboardButtonData("Вариация 3", string(midjourneyMessageInfoS7)),
								tgbotapi.NewInlineKeyboardButtonData("Вариация 4", string(midjourneyMessageInfoS8)),
							),
						)

						if replyMessageCreated {
							msg := tgbotapi.EditMessageMediaConfig{
								BaseEdit: tgbotapi.BaseEdit{
									ChatID:    chatId,
									MessageID: replyMessage.MessageID,
								},
								Media: tgbotapi.NewInputMediaPhoto(photoFileBytes),
							}
							bot.Send(msg)

							if finalResult {
								msg := tgbotapi.NewEditMessageReplyMarkup(chatId, replyMessage.MessageID, photoOptions)
								bot.Send(msg)
							}
						} else {
							msg := tgbotapi.NewPhoto(chatId, photoFileBytes)
							msg.ReplyToMessageID = update.CallbackQuery.Message.MessageID
							captions := make(map[string]string)
							captions["ZO20"] = "Расширено 2x"
							captions["ZO15"] = "Расширено 1.5x"
							msg.Caption = captions[label]
							if finalResult {
								msg.ReplyMarkup = photoOptions
							}
							replyMessage, err = bot.Send(msg)
							if err != nil {
								log.Printf("Failed to send message: %v", err)
							}
							replyMessageCreated = true
							if !finalResult {
								time.Sleep(2 * time.Second)
							}
						}
						if finalResult {
							break
						}
					}
				}
			} else {
				if contains(config.MidjourneyTranslateRUENUsernames, update.Message.From.UserName) {
					if strings.HasPrefix(messageText, "en:") || strings.HasPrefix(messageText, "En:") {
						messageText = strings.TrimPrefix(messageText, "en:")
						messageText = strings.TrimPrefix(messageText, "En:")
						msg := tgbotapi.NewMessage(chatId, messageText)
						msg.DisableWebPagePreview = true
						_, err := bot.Send(msg)
						if err != nil {
							log.Printf("Failed to send message: %v", err)
						}
					} else {
						languageCode, err := detectLanguage(config.GoogleCloudProjectName, messageText)
						if err != nil || languageCode != "en" {
							translationText, e := translateText(config.GoogleCloudProjectName, "ru", "en-US", messageText)
							if e == nil && len(translationText) > 0 {
								messageText = translationText[0]
								msg := tgbotapi.NewMessage(chatId, messageText)
								msg.DisableWebPagePreview = true
								_, err := bot.Send(msg)
								if err != nil {
									log.Printf("Failed to send message: %v", err)
								}
							}
						}
					}
				}
				prompt := messageText
				if inputPhotoUrl != "" {
					prompt = inputPhotoUrl + " " + messageText
				}
				err := MidjourneyImagine(config.MidjourneyToken, config.MidjourneyChannelId, prompt)
				if err != nil {
					msg := tgbotapi.NewMessage(chatId, "Ошибка при отправке запроса к Midjourney: "+fmt.Sprint(err))
					msg.ReplyToMessageID = update.Message.MessageID
					msg.DisableWebPagePreview = true
					_, err := bot.Send(msg)
					if err != nil {
						log.Printf("Failed to send message: %v", err)
					}
				}
				replyMessage := tgbotapi.Message{}
				replyMessageCreated := false
				for {
					result, finalResult := MidjourneyGetImagineResult(config.MidjourneyToken, config.MidjourneyChannelId, messageText, startTimeTimestamp)
					if len(result.Attachments) == 0 || result.Attachments[0].URL == "" {
						msg := tgbotapi.NewMessage(chatId, "Ошибка при отправке запроса к Midjourney")
						msg.ReplyToMessageID = update.Message.MessageID
						msg.DisableWebPagePreview = true
						bot.Send(msg)
					} else {
						fmt.Println(result.Attachments[0].URL)
						photoBytes := LoadBytesFromURL(result.Attachments[0].URL, "")
						photoFileBytes := tgbotapi.FileBytes{
							Name:  "picture",
							Bytes: photoBytes,
						}
						midjourneyMessageInfo := MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "U1",
						}
						midjourneyMessageInfoS1, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "U2",
						}
						midjourneyMessageInfoS2, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "U3",
						}
						midjourneyMessageInfoS3, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "U4",
						}
						midjourneyMessageInfoS4, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "V1",
						}
						midjourneyMessageInfoS5, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "V2",
						}
						midjourneyMessageInfoS6, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "V3",
						}
						midjourneyMessageInfoS7, _ := json.Marshal(midjourneyMessageInfo)
						midjourneyMessageInfo = MidjourneyCommandMessage{
							Id:      result.Id,
							Command: "V4",
						}
						midjourneyMessageInfoS8, _ := json.Marshal(midjourneyMessageInfo)

						var photoOptions = tgbotapi.NewInlineKeyboardMarkup(
							tgbotapi.NewInlineKeyboardRow(
								tgbotapi.NewInlineKeyboardButtonData("Увеличить 1", string(midjourneyMessageInfoS1)),
								tgbotapi.NewInlineKeyboardButtonData("Увеличить 2", string(midjourneyMessageInfoS2)),
							),
							tgbotapi.NewInlineKeyboardRow(
								tgbotapi.NewInlineKeyboardButtonData("Увеличить 3", string(midjourneyMessageInfoS3)),
								tgbotapi.NewInlineKeyboardButtonData("Увеличить 4", string(midjourneyMessageInfoS4)),
							),
							tgbotapi.NewInlineKeyboardRow(
								tgbotapi.NewInlineKeyboardButtonData("Вариация 1", string(midjourneyMessageInfoS5)),
								tgbotapi.NewInlineKeyboardButtonData("Вариация 2", string(midjourneyMessageInfoS6)),
							),
							tgbotapi.NewInlineKeyboardRow(
								tgbotapi.NewInlineKeyboardButtonData("Вариация 3", string(midjourneyMessageInfoS7)),
								tgbotapi.NewInlineKeyboardButtonData("Вариация 4", string(midjourneyMessageInfoS8)),
							),
						)

						if replyMessageCreated {
							msg := tgbotapi.EditMessageMediaConfig{
								BaseEdit: tgbotapi.BaseEdit{
									ChatID:    chatId,
									MessageID: replyMessage.MessageID,
								},
								Media: tgbotapi.NewInputMediaPhoto(photoFileBytes),
							}
							bot.Send(msg)
							msg1 := tgbotapi.NewEditMessageCaption(chatId, replyMessage.MessageID, messageText)
							bot.Send(msg1)

							if finalResult {
								msg := tgbotapi.NewEditMessageReplyMarkup(chatId, replyMessage.MessageID, photoOptions)
								bot.Send(msg)
							}
						} else {
							msg := tgbotapi.NewPhoto(chatId, photoFileBytes)
							msg.ReplyToMessageID = update.Message.MessageID
							msg.Caption = messageText
							if finalResult {
								msg.ReplyMarkup = photoOptions
							}
							replyMessage, err = bot.Send(msg)
							if err != nil {
								log.Printf("Failed to send message: %v", err)
								log.Print(photoOptions)

								msg := tgbotapi.NewMessage(chatId, "Не удалось отправить ответ от Midjourney")
								msg.ReplyToMessageID = update.Message.MessageID
								msg.DisableWebPagePreview = true
								_, err := bot.Send(msg)
								if err != nil {
									log.Printf("Failed to send message: %v", err)
								}
							}
							replyMessageCreated = true
							if !finalResult {
								time.Sleep(2 * time.Second)
							}
						}
						if finalResult {
							break
						}
					}
				}
			}
		} else {
			if update.Message == nil {
				return
			}
			generatedTextStream, err := generateTextStreamWithGPT(messageText, chatId, model)
			if err != nil {
				log.Printf("Failed to generate text stream with GPT: %v", err)
				return
			}
			text := ""
			messageID := 0
			startTime := time.Now().UTC()
			messagesCount := 0
			messageIDs := make([]int, 0)
			messages := make([]string, 0)
			for generatedText := range generatedTextStream {
				if generatedText == "" {
					continue
				}
				if text == "" {
					// Send the first message
					messagesCount++
					msgText := generatedText
					msgText2 := strings.TrimSpace(msgText) + "..."
					msg := tgbotapi.NewMessage(chatId, msgText2)
					msg.ReplyToMessageID = update.Message.MessageID
					msg.DisableWebPagePreview = true
					msg_, err := bot.Send(msg)
					if err != nil {
						log.Printf("Failed to send message: %v", err)
					}
					messageID = msg_.MessageID
					messageIDs = append(messageIDs, messageID)
					messages = append(messages, msgText2)
					fmt.Println("Message ID: ", msg_.MessageID)
					text += generatedText
					continue
				}
				text += generatedText
				if int(time.Since(startTime).Milliseconds()) < 3000 {
					continue
				}
				startTime = time.Now().UTC()

				msgText := text
				// if the length of the text is too long, send a new message
				if len(msgText) > messagesCount*4000 {
					// edit the previous message
					msgText2 := substr(msgText, (messagesCount-1)*4000, 4000)
					msgText3 := telegramPrepareMarkdownMessageV1(msgText2)
					if msgText3 != messages[messagesCount-1] {
						msg := tgbotapi.NewEditMessageText(chatId, messageID, msgText3)
						msg.ParseMode = "Markdown"
						msg.DisableWebPagePreview = true
						_, err := bot.Send(msg)
						if err != nil {
							log.Printf("Failed to edit message: %v", err)
							msgText3 = strings.TrimSpace(msgText2) + "..."
							if msgText3 != messages[messagesCount-1] {
								msg := tgbotapi.NewEditMessageText(chatId, messageID, msgText3)
								msg.DisableWebPagePreview = true
								_, err = bot.Send(msg)
								messages[messagesCount-1] = msgText3
							}
						}
					}
					messages[messagesCount-1] = msgText3

					// Create new message
					messagesCount++
					msgText2 = substr(msgText, (messagesCount-1)*4000, 4000)
					msgText3 = strings.TrimSpace(telegramPrepareMarkdownMessageV1(msgText2)) + "..."
					msgNew := tgbotapi.NewMessage(chatId, msgText3)
					msgNew.ParseMode = "Markdown"
					msgNew.ReplyToMessageID = messageID
					msgNew.DisableWebPagePreview = true
					msg_, err := bot.Send(msgNew)
					if err != nil {
						log.Printf("Failed to send message: %v", err)
						msgText3 = strings.TrimSpace(msgText2) + "..."
						msg := tgbotapi.NewEditMessageText(chatId, messageID, msgText3)
						msg.DisableWebPagePreview = true
						msg_, err = bot.Send(msg)
						messageID = msg_.MessageID
						messageIDs = append(messageIDs, messageID)
						messages = append(messages, msgText3)
						continue
					}
					messageID = msg_.MessageID
					messageIDs = append(messageIDs, messageID)
					messages = append(messages, msgText2)
					continue
				}

				// Update all messages
				for i, messageID := range messageIDs {
					msgText2 := ""
					msgText2 = substr(msgText, i*4000, 4000)
					msgText3 := strings.TrimSpace(telegramPrepareMarkdownMessageV1(msgText2))
					if i == len(messageIDs)-1 {
						msgText3 += "..."
					}
					if msgText3 == messages[i] {
						continue
					}
					msg := tgbotapi.NewEditMessageText(chatId, messageID, msgText3)
					msg.ParseMode = "Markdown"
					msg.DisableWebPagePreview = true
					_, err = bot.Send(msg)
					if err != nil {
						log.Printf("Failed to edit message (Markdown): %v, message: %s", err, msgText2)
						msgText3 = strings.TrimSpace(msgText2)
						if i == len(messageIDs)-1 {
							msgText3 += "..."
						}
						if msgText3 == messages[i] {
							continue
						}
						msg := tgbotapi.NewEditMessageText(chatId, messageID, msgText3)
						msg.DisableWebPagePreview = true
						_, err = bot.Send(msg)
						if err != nil {
							log.Printf("Failed to edit message (Plaintext): %v, message: %s", err, msgText2)
						}
						messages[i] = msgText3
					}
					messages[i] = msgText3
				}
				continue
			}
			msgText := text
			//fmt.Println("Whole text:\n\n", msgText)
			//fmt.Println("Whole message:\n\n", msgText)
			// Update all messages
			for i, messageID := range messageIDs {
				text = substr(msgText, i*4000, 4000)
				msgText3 := strings.TrimSpace(telegramPrepareMarkdownMessageV1(text))
				if msgText3 == messages[i] {
					continue
				}
				msg := tgbotapi.NewEditMessageText(chatId, messageID, msgText3)
				msg.ParseMode = "Markdown"
				msg.DisableWebPagePreview = true
				_, err = bot.Send(msg)
				if err != nil {
					log.Printf("Failed to edit message (Markdown): %v, message: %s", err, text)
					msgText3 = strings.TrimSpace(text)
					if msgText3 == messages[i] {
						continue
					}
					msg := tgbotapi.NewEditMessageText(chatId, messageID, text)
					msg.DisableWebPagePreview = true
					_, err = bot.Send(msg)
					if err != nil {
						log.Printf("Failed to edit message (Plaintext): %v, message: %s", err, text)
					}
				}
			}
		}
	}
	CompleteResponse(chatId)
}

func handleCommand(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	command := update.Message.Command()
	commandArg := update.Message.CommandArguments()
	switch command {
	case "start":
		// Reset the conversation history for the user
		mu.Lock()
		model := ""
		model = DefaultModel
		conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{
			{
				Role:    "system",
				Content: DefaultSystemPrompt,
			},
		}
		userSettingsMap[update.Message.Chat.ID] = User{
			Model:        model,
			SystemPrompt: DefaultSystemPrompt,
		}
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Добро пожаловать в GPT Телеграм-бот!")
		msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
		bot.Send(msg)
	case "new":
		// Reset the conversation history for the user
		mu.Lock()
		conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{}
		/*
			conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{
				{
					Role:    "system",
					Content: userSettingsMap[update.Message.Chat.ID].SystemPrompt,
				},
			}*/

		userSettingsMap[update.Message.Chat.ID].BardChatbot.Reset()
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "История беседы очищена.")
		msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
		bot.Send(msg)
	case "gpt4":
		mu.Lock()
		userSettingsMap[update.Message.Chat.ID] = User{
			Model: GPT4Model1106,
		}
		// Reset the conversation history for the user
		conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{}
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Включена модель *OpenAI GPT 4*\\.")
		msg.ParseMode = "MarkdownV2"
		msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
		bot.Send(msg)
	case "gpt35":
		mu.Lock()
		userSettingsMap[update.Message.Chat.ID] = User{
			Model: GPT35TurboModel16k,
		}
		// Reset the conversation history for the user
		conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{
			{
				Role:    "system",
				Content: userSettingsMap[update.Message.Chat.ID].SystemPrompt,
			},
		}
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Включена модель *OpenAI GPT 3\\.5*\\.")
		msg.ParseMode = "MarkdownV2"
		msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
		bot.Send(msg)
	case "bard":
		if !contains(config.BardAllowedUsers, update.Message.From.UserName) {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Вам нельзя пользоваться моделью *Google Bard*\\.")
			msg.ParseMode = "MarkdownV2"
			bot.Send(msg)
			return
		}
		mu.Lock()
		chatbot := BardNewChatbot(config.BardSession)
		if chatbot == nil {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Не удалось включить модель *Google Bard*\\.")
			msg.ParseMode = "MarkdownV2"
			bot.Send(msg)
			return
		}
		userSettingsMap[update.Message.Chat.ID] = User{
			Model:       BardModel,
			BardChatbot: chatbot,
		}
		// Reset the conversation history for the user
		conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{}
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, `Включена модель *Google Bard* \(`+telegramPrepareMarkdownMessageV2(chatbot.sessionBl)+`\)\.`)
		msg.ParseMode = "MarkdownV2"
		msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
		_, err := bot.Send(msg)
		_ = err
	case "dalle":
		mu.Lock()
		userSettingsMap[update.Message.Chat.ID] = User{
			Model: DalleModel,
		}
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Включена модель *OpenAI DALL\\-E 3*\\.")
		msg.ParseMode = "MarkdownV2"
		msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
		bot.Send(msg)
	case "midjourney":
		mu.Lock()
		userSettingsMap[update.Message.Chat.ID] = User{
			Model: MidjourneyModel,
		}
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Включена модель *Midjourney*\\.")
		msg.ParseMode = "MarkdownV2"
		msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
		bot.Send(msg)
	case "retry":
		break
		// Retry the last message
		mu.Lock()
		lastMessage := conversationHistory[update.Message.Chat.ID][len(conversationHistory[update.Message.Chat.ID])-2]
		conversationHistory[update.Message.Chat.ID] = conversationHistory[update.Message.Chat.ID][:len(conversationHistory[update.Message.Chat.ID])-2]
		model := userSettingsMap[update.Message.Chat.ID].Model
		if model == "" {
			model = DefaultModel
		}
		mu.Unlock()
		generatedText, err := generateTextWithGPT(lastMessage.Content, update.Message.Chat.ID, model)
		if err != nil {
			log.Printf("Failed to generate text with GPT: %v", err)
			return
		}
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, generatedText)
		msg.ReplyToMessageID = update.Message.MessageID
		msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
		bot.Send(msg)
	case "stop":
		mu.Lock()
		user := userSettingsMap[update.Message.Chat.ID]
		if user.CurrentContext != nil {
			CompleteResponse(update.Message.Chat.ID)
		}
		mu.Unlock()
	case "system_prompt":
		if commandArg == "" {
			mu.Lock()
			userSettingsMap[update.Message.Chat.ID] = User{
				Model:        userSettingsMap[update.Message.Chat.ID].Model,
				SystemPrompt: userSettingsMap[update.Message.Chat.ID].SystemPrompt,
				State:        StateWaitingForSystemPrompt,
			}
			mu.Unlock()
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Напишите системный промпт.")
			msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
			bot.Send(msg)
			return
		}
		mu.Lock()
		userSettingsMap[update.Message.Chat.ID] = User{
			Model:        userSettingsMap[update.Message.Chat.ID].Model,
			SystemPrompt: commandArg,
		}

		conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{
			{
				Role:    "system",
				Content: commandArg,
			},
		}

		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Установлен системный промпт: %s", commandArg))
		bot.Send(msg)
	default:
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Неизвестная команда: %s", command))
		bot.Send(msg)
	}
}

func generateTextWithGPT(inputText string, chatID int64, model string) (string, error) {
	// Add the user's message to the conversation history
	conversationHistory[chatID] = append(conversationHistory[chatID], gpt3.ChatCompletionRequestMessage{
		Role:    "user",
		Content: inputText,
	})

	temp := float32(0.7)
	maxTokens := 4096
	if model == GPT4Model || model == GPT4Model0613 {
		maxTokens = 8192
	}
	if model == GPT4Model1106 {
		maxTokens = 4096
	}
	e, err := tokenizer.NewEncoder()
	if err != nil {
		return "", fmt.Errorf("failed to create encoder: %w", err)
	}
	totalTokens := 0
	if model != GPT4Model1106 {
		for _, message := range conversationHistory[chatID] {
			q, err := e.Encode(message.Content)
			if err != nil {
				return "", fmt.Errorf("failed to encode message: %w", err)
			}
			totalTokens += len(q)
			q, err = e.Encode(message.Role)
			if err != nil {
				return "", fmt.Errorf("failed to encode message: %w", err)
			}
			totalTokens += len(q)
		}
	}
	maxTokens -= totalTokens
	request := gpt3.ChatCompletionRequest{
		Model:       model,
		Messages:    conversationHistory[chatID],
		Temperature: &temp,
		MaxTokens:   maxTokens,
		TopP:        1,
	}
	ctx := context.Background()

	// Call the OpenAI API
	var response *gpt3.ChatCompletionResponse
	//if model == GPT4Model || model == GPT4BrowsingModel {
	//	response, err = openaiClientGPT4.ChatCompletion(ctx, request)
	//} else {
	response, err = openaiClient.ChatCompletion(ctx, request)
	//}
	if err != nil {
		return "", fmt.Errorf("failed to call OpenAI API: %w", err)
	}

	// Get the generated text
	generatedText := response.Choices[0].Message.Content
	generatedText = strings.TrimSpace(generatedText)

	// Add the AI's response to the conversation history
	conversationHistory[chatID] = append(conversationHistory[chatID], gpt3.ChatCompletionRequestMessage{
		Role:    "assistant",
		Content: generatedText,
	})

	return generatedText, nil
}

func generateTextStreamWithGPT(inputText string, chatID int64, model string) (chan string, error) {
	// Add the user's message to the conversation history
	conversationHistory[chatID] = append(conversationHistory[chatID], gpt3.ChatCompletionRequestMessage{
		Role:    "user",
		Content: inputText,
	})
	conversationFunctions := []gpt3.ChatCompletionRequestFunction{}

	e, err := tokenizer.NewEncoder()
	if err != nil {
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	totalTokensForFunctions := 0
	for _, function := range functions {
		if function.Active == 0 || function.Default == 0 {
			continue
		}
		conversationFunction := gpt3.ChatCompletionRequestFunction{}
		conversationFunction.Name = function.Name
		conversationFunction.Description = function.Description
		conversationFunction.Parameters = gpt3.ChatCompletionRequestFunctionParameters{
			Type:       "object",
			Properties: function.Args.Properties,
			Required:   function.Args.Required,
		}
		conversationFunction.FunctionCall = "auto"
		conversationFunctions = append(conversationFunctions, conversationFunction)

		functionS, _ := json.Marshal(conversationFunction)
		q, err := e.Encode(string(functionS))
		if err != nil {
			return nil, fmt.Errorf("failed to encode message: %w", err)
		}
		totalTokensForFunctions += len(q)
	}
	temp := float32(0.7)
	request := gpt3.ChatCompletionRequest{
		Model:       model,
		Messages:    conversationHistory[chatID],
		Temperature: &temp,
		TopP:        1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(10*time.Minute))
	mu.Lock()
	user := userSettingsMap[chatID]
	user.CurrentContext = &cancel
	mu.Unlock()
	response := make(chan string)
	// Call the OpenAI API
	go func() {
		var err error
		/*
			if model == GPT4Model || model == GPT4BrowsingModel {
				maxTokens := 8192

				totalTokens := 0
				for _, message := range conversationHistory[chatID] {
					q, err := e.Encode(message.Content)
					if err != nil {
						return // nil, fmt.Errorf("failed to encode message: %w", err)
					}
					totalTokens += len(q)
					q, err = e.Encode(message.Role)
					if err != nil {
						return // nil, fmt.Errorf("failed to encode message: %w", err)
					}
					totalTokens += len(q)
				}
				maxTokens -= totalTokens + 100
				request.MaxTokens = maxTokens

				err = openaiClientGPT4.ChatCompletionStream(ctx, request, func(completion *gpt3.ChatCompletionStreamResponse) {
					log.Printf("Received completion: %v\n", completion)
					response <- completion.Choices[0].Delta.Content
					mu.Lock()
					user := userSettingsMap[chatID]
					user.CurrentMessageBuffer += completion.Choices[0].Delta.Content
					userSettingsMap[chatID] = user
					mu.Unlock()
					if completion.Choices[0].FinishReason != "" {
						mu.Lock()
						user := userSettingsMap[chatID]
						if user.Model == GPT4BrowsingModel {
							user.CurrentMessageBuffer = GPT4BrowsingReplaceMetadata(user.CurrentMessageBuffer, true)
						}
						userSettingsMap[chatID] = user
						mu.Unlock()
						CompleteResponse(chatID)
						close(response)
					}
				})
			} else {
		*/
		functionCallHistory := make(map[string]bool)
		for {
			maxTokens := 4096
			if model == GPT4Model || model == GPT4Model0613 {
				maxTokens = 8192
			} else if model == GPT35TurboModel16k {
				maxTokens = 16384
			} else if model == GPT4Model1106 {
				maxTokens = 4096
			}
			totalTokens := 0
			if model != GPT4Model1106 {
				for _, message := range conversationHistory[chatID] {
					q, err := e.Encode(message.Content)
					if err != nil {
						return // nil, fmt.Errorf("failed to encode message: %w", err)
					}
					totalTokens += len(q)
					q, err = e.Encode(message.Role)
					if err != nil {
						return // nil, fmt.Errorf("failed to encode message: %w", err)
					}
					totalTokens += len(q)
				}
				maxTokens -= totalTokens + totalTokensForFunctions + 100
			}

			if maxTokens < 10 {
				response <- "Ошибка: закончился размер контекста, использовано " + fmt.Sprint(totalTokens+totalTokensForFunctions) + " токенов.\n\n"
				break
			}

			request.MaxTokens = maxTokens
			request.Functions = conversationFunctions
			request.Messages = conversationHistory[chatID]
			functionCallName := ""
			functionCallArgs := ""
			err = openaiClient.ChatCompletionStream(ctx, request, func(completion *gpt3.ChatCompletionStreamResponse) {
				if len(completion.Choices) > 0 {
					log.Printf("Received completion: %v\n", completion)
					if completion.Choices[0].Delta.FunctionCall.Name != "" ||
						completion.Choices[0].Delta.FunctionCall.Arguments != "" {
						functionCallName += completion.Choices[0].Delta.FunctionCall.Name
						functionCallArgs += completion.Choices[0].Delta.FunctionCall.Arguments
					} else {
						if completion.Choices[0].Delta.Content != "" {
							response <- completion.Choices[0].Delta.Content
						}
					}
					mu.Lock()
					user := userSettingsMap[chatID]
					user.CurrentMessageBuffer += completion.Choices[0].Delta.Content
					userSettingsMap[chatID] = user
					mu.Unlock()
					if completion.Choices[0].FinishReason != "" {
						if functionCallName != "" {
							conversationHistory[chatID] = append(conversationHistory[chatID], gpt3.ChatCompletionRequestMessage{
								Role:    "assistant",
								Content: "",
								FunctionCall: gpt3.ChatCompletionResponseFunctionCall{
									Name:      functionCallName,
									Arguments: functionCallArgs,
								},
							})
							response <- functionCallName + "(" + functionCallArgs + ")\n\n"
						} else {
							mu.Lock()
							user := userSettingsMap[chatID]
							userSettingsMap[chatID] = user
							mu.Unlock()
							CompleteResponse(chatID)
							close(response)
						}
					}
				}
			})
			if err != nil && strings.Contains(err.Error(), "429:tokens") {
				delay := 10 * time.Second
				log.Printf("Rate limit reached, waiting %v\n", delay)
				time.Sleep(delay)
				continue
			}
			if functionCallName != "" {
				if functionCallHistory[functionCallName+functionCallArgs] {
					output := "Ошибка вызова функции: повторный вызов функции с одними и теми же аргументами"
					functionCallHistory[functionCallName+functionCallArgs] = true
					response <- output + "\n\n"
					conversationHistory[chatID] = append(conversationHistory[chatID], gpt3.ChatCompletionRequestMessage{
						Role:    "function",
						Content: output,
						Name:    functionCallName,
					})
					functionCallName = ""
					functionCallArgs = ""
					continue
				}
				output, err := CallFunction(functionCallName, functionCallArgs)
				functionCallHistory[functionCallName+functionCallArgs] = true
				if err != nil {
					response <- "Ошибка вызова функции: " + err.Error() + "\n\n"
				} else {
					//response <- output + "\n\n"
					conversationHistory[chatID] = append(conversationHistory[chatID], gpt3.ChatCompletionRequestMessage{
						Role:    "function",
						Content: output,
						Name:    functionCallName,
					})
					functionCallName = ""
					functionCallArgs = ""
				}
			} else {
				break
			}
		}
		if err != nil {
			response <- "failed to call OpenAI API: " + err.Error()
			// if response open, close it
			if _, ok := <-response; ok {

				close(response)
			}
			// return nil, fmt.Errorf("failed to call OpenAI API: %w", err)
		}
	}()

	return response, nil
}

func CompleteResponse(chatID int64) {
	mu.Lock()
	user := userSettingsMap[chatID]
	generatedText := user.CurrentMessageBuffer
	user.CurrentMessageBuffer = ""
	userSettingsMap[chatID] = user

	// Get the generated text
	generatedText = strings.TrimSpace(generatedText)

	// Add the AI's response to the conversation history
	if generatedText != "" {
		conversationHistory[chatID] = append(conversationHistory[chatID], gpt3.ChatCompletionRequestMessage{
			Role:    "assistant",
			Content: generatedText,
		})
	}

	if user.CurrentContext == nil {
		mu.Unlock()
		return
	}
	(*user.CurrentContext)()
	user.CurrentContext = nil
	mu.Unlock()
}
