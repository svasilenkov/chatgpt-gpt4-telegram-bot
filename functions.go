package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/jaytaylor/html2text"
	googlesearch "github.com/rocketlaunchr/google-search"
)

type FunctionArgs struct {
	Properties map[string]map[string]string `json:"properties"`
	Required   []string                     `json:"required"`
}

type Function struct {
	Id          int          `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Args        FunctionArgs `json:"args"`
	Default     int          `json:"default"`
	Active      int          `json:"active"`
}

var functions = []Function{
	{
		Id:          1,
		Name:        "http_get",
		Description: "Load data from the Internet, HTML is converted to text",
		Args: FunctionArgs{
			Properties: map[string]map[string]string{
				"url": {
					"type":        "string",
					"description": "URL to load data from",
				},
			},
			Required: []string{"url"},
		},
		Default: 1,
		Active:  1,
	},
	{
		Id:          2,
		Name:        "google_search",
		Description: "Search Google",
		Args: FunctionArgs{
			Properties: map[string]map[string]string{
				"query": {
					"type":        "string",
					"description": "Query to search",
				},
			},
			Required: []string{"query"},
		},
		Default: 1,
		Active:  1,
	},
}

func JsonEscape(str string) string {
	return strings.ReplaceAll(strings.ReplaceAll(str, `\`, `\\`), `"`, `\"`)
}

func ParseArguments(args string) (map[string]string, error) {
	arguments := make(map[string]string)
	argumentsS := args
	if len(argumentsS) > 0 && argumentsS[len(argumentsS)-1] != '}' {
		argumentsS += "}"
	}
	err := json.Unmarshal([]byte(argumentsS), &arguments)
	if err != nil {
		return nil, errors.New("Ошибка парсинга аргументов: " + err.Error())
	}
	return arguments, nil
}

func CallFunction(name string, args string) (string, error) {
	switch name {
	case "http_get":
		arguments, err := ParseArguments(args)
		if err != nil {
			return "", err
		}
		return HttpGet(arguments["url"])
	case "google_search":
		arguments, err := ParseArguments(args)
		if err != nil {
			return "", err
		}
		return GoogleSearch(arguments["query"])
	default:
		return "", errors.New("Неизвестная функция " + name)
	}
}

func HttpGet(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	text, _ := html2text.FromString(string(body), html2text.Options{OmitLinks: true, TextOnly: true})
	return `{
		"output": "` + JsonEscape(strings.TrimSuffix(string(text), "\n")) + `"
	}`, nil
}

func GoogleSearch(query string) (string, error) {
	ctx := context.Background()
	result, _ := googlesearch.Search(ctx, query)
	resultS, _ := json.Marshal(result)
	return `{
		"result": "` + JsonEscape(string(resultS)) + `"
	}`, nil
}
