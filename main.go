package main

import (
	"bytes"
	"context"
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

	"gopkg.in/yaml.v2"

	openai "github.com/0x9ef/openai-go"
	"github.com/PullRequestInc/go-gpt3"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	tokenizer "github.com/samber/go-gpt-3-encoder"
)

const (
	GPT4Model       = "gpt-4"
	GPT35TurboModel = "gpt-3.5-turbo"
	BardModel       = "bard"
)

const DefaultModel = GPT35TurboModel
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
	DebugMode        string   `yaml:"debug_mode"`
	TelegramToken    string   `yaml:"telegram_token"`
	OpenAIKey        string   `yaml:"openai_api_key"`
	BardSession      string   `yaml:"bard_session_id"`
	AllowedUsers     []string `yaml:"allowed_telegram_usernames"`
	BardAllowedUsers []string `yaml:"bard_allowed_telegram_usernames"`
	GPT4AllowedUsers []string `yaml:"gpt4_allowed_telegram_usernames"`
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

var openaiClient gpt3.Client

func main() {
	var err error
	config, err = ReadConfig()
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
	}
	// Initialize the OpenAI API client

	if DefaultModel == GPT4Model {
		openaiClient = gpt3.NewClient(config.OpenAIKey, gpt3.WithBaseURL(os.Getenv("CUSTOM_OPENAI_API_ENDPOINT")+"/v1"))
	} else {
		openaiClient = gpt3.NewClient(config.OpenAIKey)
	}

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

	updates, err := bot.GetUpdatesChan(u)
	if err != nil {
		log.Fatalf("Failed to get updates channel: %v", err)
	}

	// Handle updates
	for update := range updates {
		go func(update tgbotapi.Update) {
			if update.Message == nil {
				return
			}
			if update.Message.IsCommand() {
				handleCommand(bot, update)
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

func telegramPrepareMarkdownMessage(msg string) string {
	result := msg

	entities := []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}

	for _, entity := range entities {
		result = strings.ReplaceAll(result, entity, `\`+entity)
	}
	return result
}

func handleMessage(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	if !contains(config.AllowedUsers, update.Message.From.UserName) {
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Вам нельзя пользоваться этим ботом")
		bot.Send(msg)
		return
	}
	mu.Lock()
	state := userSettingsMap[update.Message.Chat.ID].State
	model := userSettingsMap[update.Message.Chat.ID].Model
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
	messageText := update.Message.Text
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
		_, err := bot.Send(msg)
		if err != nil {
			log.Printf("Failed to send message: %v", err)
		}
	}
	if messageText != "" {
		if userSettingsMap[update.Message.Chat.ID].Model == BardModel {
			response, err := userSettingsMap[update.Message.Chat.ID].BardChatbot.Ask(messageText)
			if err != nil {
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Ошибка обращаения к Bard: "+err.Error())
				msg.ReplyToMessageID = update.Message.MessageID
				_, err := bot.Send(msg)
				if err != nil {
					fmt.Println(err)
				}
			} else {
				response = userSettingsMap[update.Message.Chat.ID].BardChatbot.PrepareForTelegramMarkdown(response)
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, response)
				msg.ParseMode = "Markdown"
				msg.ReplyToMessageID = update.Message.MessageID
				_, err := bot.Send(msg)
				if err != nil {
					log.Printf("Failed to send message as Markdown: %v"+response, err)
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, response)
					msg.ReplyToMessageID = update.Message.MessageID
					_, err := bot.Send(msg)
					if err != nil {
						log.Printf("Failed to send message: %v", err)
					}
				}
			}
		} else {
			generatedTextStream, err := generateTextStreamWithGPT(messageText, update.Message.Chat.ID, model)
			if err != nil {
				log.Printf("Failed to generate text stream with GPT: %v", err)
				return
			}
			text := ""
			messageID := 0
			startTime := time.Now().UTC()
			for generatedText := range generatedTextStream {
				if generatedText == "" {
					continue
				}
				if text == "" {
					// Send the first message
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, generatedText+"...")
					msg.ReplyToMessageID = update.Message.MessageID
					msg_, err := bot.Send(msg)
					if err != nil {
						log.Printf("Failed to send message: %v", err)
					}
					messageID = msg_.MessageID
					fmt.Println("Message ID: ", msg_.MessageID)
					text += generatedText
					continue
				}
				text += generatedText
				// if the length of the text is too long, send a new message
				if len(text) > 4096 {
					text = generatedText
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
					msg.ReplyToMessageID = messageID
					msg_, err := bot.Send(msg)
					if err != nil {
						log.Printf("Failed to send message: %v", err)
					}
					messageID = msg_.MessageID
					continue
				}
				// Edit the message
				if int(time.Since(startTime).Milliseconds()) < 1000 {
					continue
				}
				startTime = time.Now().UTC()
				msg := tgbotapi.NewEditMessageText(update.Message.Chat.ID, messageID, text+"...")
				msg.ParseMode = "Markdown"
				_, err := bot.Send(msg)
				if err != nil {
					log.Printf("Failed to edit message: %v", err)
					continue
				}
			}
			msg := tgbotapi.NewEditMessageText(update.Message.Chat.ID, messageID, text)
			msg.ParseMode = "Markdown"
			_, err = bot.Send(msg)
			if err != nil {
				log.Printf("Failed to edit message: %v", err)
			}
		}
	}
	CompleteResponse(update.Message.Chat.ID)
}

func handleCommand(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	command := update.Message.Command()
	commandArg := update.Message.CommandArguments()
	switch command {
	case "start":
		// Reset the conversation history for the user
		mu.Lock()
		conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{
			{
				Role:    "system",
				Content: DefaultSystemPrompt,
			},
		}
		userSettingsMap[update.Message.Chat.ID] = User{
			Model:        DefaultModel,
			SystemPrompt: DefaultSystemPrompt,
		}
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Добро пожаловать в GPT Телеграм-бот!")
		bot.Send(msg)
	case "new":
		// Reset the conversation history for the user
		mu.Lock()
		conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{
			{
				Role:    "system",
				Content: userSettingsMap[update.Message.Chat.ID].SystemPrompt,
			},
		}
		userSettingsMap[update.Message.Chat.ID].BardChatbot.Reset()
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "История беседы очищена.")
		bot.Send(msg)
	case "gpt4":
		if !contains(config.GPT4AllowedUsers, update.Message.From.UserName) {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Вам нельзя пользоваться моделью *Google Bard*\\.")
			msg.ParseMode = "MarkdownV2"
			bot.Send(msg)
			return
		}
		mu.Lock()
		userSettingsMap[update.Message.Chat.ID] = User{
			Model: GPT4Model,
		}
		// Reset the conversation history for the user
		conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{
			{
				Role:    "system",
				Content: userSettingsMap[update.Message.Chat.ID].SystemPrompt,
			},
		}
		openaiClient = gpt3.NewClient(config.OpenAIKey, gpt3.WithBaseURL(os.Getenv("CUSTOM_OPENAI_API_ENDPOINT")+"/v1"))
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Включена модель *OpenAI GPT\\-4*\\.")
		msg.ParseMode = "MarkdownV2"
		bot.Send(msg)
	case "gpt35":
		mu.Lock()
		userSettingsMap[update.Message.Chat.ID] = User{
			Model: GPT35TurboModel,
		}
		// Reset the conversation history for the user
		conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{
			{
				Role:    "system",
				Content: userSettingsMap[update.Message.Chat.ID].SystemPrompt,
			},
		}
		openaiClient = gpt3.NewClient(config.OpenAIKey)
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Включена модель *OpenAI GPT\\-3\\.5\\-turbo*\\.")
		msg.ParseMode = "MarkdownV2"
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
		conversationHistory[update.Message.Chat.ID] = []gpt3.ChatCompletionRequestMessage{
			{
				Role:    "system",
				Content: userSettingsMap[update.Message.Chat.ID].SystemPrompt,
			},
		}
		mu.Unlock()
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, `Включена модель *Google Bard* \(`+telegramPrepareMarkdownMessage(chatbot.sessionBl)+`\)\.`)
		msg.ParseMode = "MarkdownV2"
		_, err := bot.Send(msg)
		_ = err
	case "retry":
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
	if model == GPT4Model {
		maxTokens = 8192
	}
	e, err := tokenizer.NewEncoder()
	if err != nil {
		return "", fmt.Errorf("failed to create encoder: %w", err)
	}
	totalTokens := 0
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
	response, err := openaiClient.ChatCompletion(ctx, request)
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

	temp := float32(0.7)
	maxTokens := 4096
	if model == GPT4Model {
		maxTokens = 8192
	}
	e, err := tokenizer.NewEncoder()
	if err != nil {
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}
	totalTokens := 0
	for _, message := range conversationHistory[chatID] {
		q, err := e.Encode(message.Content)
		if err != nil {
			return nil, fmt.Errorf("failed to encode message: %w", err)
		}
		totalTokens += len(q)
		q, err = e.Encode(message.Role)
		if err != nil {
			return nil, fmt.Errorf("failed to encode message: %w", err)
		}
		totalTokens += len(q)
	}
	maxTokens -= totalTokens + 100
	request := gpt3.ChatCompletionRequest{
		Model:       model,
		Messages:    conversationHistory[chatID],
		Temperature: &temp,
		MaxTokens:   maxTokens,
		TopP:        1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*60*time.Second)
	mu.Lock()
	user := userSettingsMap[chatID]
	user.CurrentContext = &cancel
	mu.Unlock()
	response := make(chan string)
	// Call the OpenAI API
	go func() {
		err := openaiClient.ChatCompletionStream(ctx, request, func(completion *gpt3.ChatCompletionStreamResponse) {
			log.Printf("Received completion: %v\n", completion)
			response <- completion.Choices[0].Delta.Content
			mu.Lock()
			user := userSettingsMap[chatID]
			user.CurrentMessageBuffer += completion.Choices[0].Delta.Content
			userSettingsMap[chatID] = user
			mu.Unlock()
			if completion.Choices[0].FinishReason != "" {
				close(response)
				CompleteResponse(chatID)
			}
		})
		if err != nil {
			// if response open, close it
			if _, ok := <-response; ok {
				response <- "failed to call OpenAI API"
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
	if user.CurrentContext == nil {
		mu.Unlock()
		return
	}
	(*user.CurrentContext)()
	user.CurrentContext = nil
	generatedText := user.CurrentMessageBuffer
	user.CurrentMessageBuffer = ""
	userSettingsMap[chatID] = user
	mu.Unlock()

	// Get the generated text
	generatedText = strings.TrimSpace(generatedText)

	// Add the AI's response to the conversation history
	conversationHistory[chatID] = append(conversationHistory[chatID], gpt3.ChatCompletionRequestMessage{
		Role:    "assistant",
		Content: generatedText,
	})
}
