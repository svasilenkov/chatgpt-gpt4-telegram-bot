package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
)

type BardChatbot struct {
	sessionId       string
	sessionAt       string
	sessionBl       string
	headers         http.Header
	reqid           int
	conversation_id string
	response_id     string
	choice_id       string
}

func BardNewChatbot(sessionId string) *BardChatbot {
	rand.Seed(time.Now().UnixNano())

	sessionAtAndBl, err := getSessionAtAndBl(sessionId)
	if err != nil {
		return nil
	}

	return &BardChatbot{
		sessionId: sessionId,
		sessionAt: sessionAtAndBl[0],
		sessionBl: sessionAtAndBl[1],
		headers: http.Header{
			"Host":                      []string{"bard.google.com"},
			"X-Same-Domain":             []string{"1"},
			"User-Agent":                []string{"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.114 Safari/537.36"},
			"Content-Type":              []string{"application/x-www-form-urlencoded;charset=UTF-8"},
			"Origin":                    []string{"https://bard.google.com"},
			"Referer":                   []string{"https://bard.google.com/"},
			"Accept":                    []string{"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
			"Accept-Language":           []string{"en-US,en;q=0.5"},
			"Connection":                []string{"keep-alive"},
			"Upgrade-Insecure-Requests": []string{"1"},
			"Sec-Fetch-Dest":            []string{"document"},
			"Sec-Fetch-Mode":            []string{"navigate"},
			"Sec-Fetch-Site":            []string{"none"},
			"TE":                        []string{"trailers"},
			"Cookie":                    []string{fmt.Sprintf("__Secure-1PSID=%s", sessionId)},
		},
		reqid: rand.Intn(10000),
	}
}

func getSessionAtAndBl(sessionId string) ([]string, error) {
	req, err := http.NewRequest("GET", "https://bard.google.com/", nil)
	if err != nil {
		return []string{"", ""}, err
	}
	req.Header = http.Header{
		"Host":                      []string{"bard.google.com"},
		"X-Same-Domain":             []string{"1"},
		"User-Agent":                []string{"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.114 Safari/537.36"},
		"Content-Type":              []string{"application/x-www-form-urlencoded;charset=UTF-8"},
		"Origin":                    []string{"https://bard.google.com"},
		"Referer":                   []string{"https://bard.google.com/"},
		"Accept":                    []string{"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
		"Accept-Language":           []string{"en-US,en;q=0.5"},
		"Connection":                []string{"keep-alive"},
		"Upgrade-Insecure-Requests": []string{"1"},
		"Sec-Fetch-Dest":            []string{"document"},
		"Sec-Fetch-Mode":            []string{"navigate"},
		"Sec-Fetch-Site":            []string{"none"},
		"TE":                        []string{"trailers"},
		"Cookie":                    []string{fmt.Sprintf("__Secure-1PSID=%s", sessionId)},
	}

	// Send the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return []string{"", ""}, err
	}
	if resp.StatusCode != 200 {
		return []string{"", ""}, fmt.Errorf("Error: %d", resp.StatusCode)
	}

	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []string{"", ""}, err
	}

	matchAt := regexp.MustCompile(`SNlM0e":"(.*?)"`).FindStringSubmatch(string(content))
	if len(matchAt) != 2 {
		fmt.Println(string(content))
		return []string{"", ""}, errors.New("Could not find SNlM0e")
	}

	matchBl := regexp.MustCompile(`cfb2h":"(.*?)"`).FindStringSubmatch(string(content))
	if len(matchBl) != 2 {
		fmt.Println(string(content))
		return []string{"", ""}, errors.New("Could not find cfb2h")
	}

	return []string{matchAt[1], matchBl[1]}, nil
}

func (c *BardChatbot) Ask(message string) (string, error) {
	messageStruct := [][]interface{}{
		{message},
		nil,
		{c.conversation_id, c.response_id, c.choice_id},
	}

	messageStructJson, _ := json.Marshal(messageStruct)

	params := map[string]string{
		"f.req": `[null, "` + strings.ReplaceAll(strings.ReplaceAll(string(messageStructJson), `\`, `\\`), `"`, `\"`) + `"]`,
		"at":    c.sessionAt,
	}

	values := make(url.Values)

	for key, val := range params {
		values.Add(key, val)
	}

	// Make the request
	req, err := http.NewRequest("POST", "https://bard.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?bl="+url.QueryEscape(c.sessionBl)+"&_reqid="+fmt.Sprintf("%04d", c.reqid)+"&rt=c", strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	req.Header = c.headers

	// Send the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Error: %d", resp.StatusCode)
	}

	// Read the response body
	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	log.Printf("Bard response: %s\n", content)

	// Split the response body
	split := strings.Split(string(content), "\n")
	if len(split) < 4 {
		return "", fmt.Errorf("Error: invalid response")
	}
	wholeResponse := [][]interface{}{}

	// Parse the response body
	if err := json.Unmarshal([]byte(split[3]), &wholeResponse); err != nil {
		return "", err
	}

	responseData := []interface{}{}

	if len(wholeResponse) < 1 || len(wholeResponse[0]) < 3 {
		return "", fmt.Errorf("Error: invalid response")
	}
	if wholeResponse[0][2] == nil {
		return "", fmt.Errorf("Error: invalid response")
	}

	structS := wholeResponse[0][2].(string)
	fmt.Println(structS)
	if err := json.Unmarshal([]byte(structS), &responseData); err != nil {
		return "", err
	}
	choices := []interface{}{}
	if len(responseData) < 5 || len(responseData[4].([]interface{})) < 1 {
		return "", fmt.Errorf("Error: invalid response")
	}
	choices = (responseData[4].([]interface{}))[0].([]interface{})
	if len(choices) < 1 {
		return "", fmt.Errorf("Error: invalid response")
	}
	c.conversation_id = (responseData[1].([]interface{}))[0].(string)
	c.response_id = (responseData[1].([]interface{}))[1].(string)
	c.choice_id = choices[0].(string)
	c.reqid += 100000

	return (responseData[0].([]interface{}))[0].(string), nil
}

func (c *BardChatbot) Reset() {
	if c == nil {
		return
	}
	c.conversation_id = ""
	c.response_id = ""
	c.choice_id = ""
}

func (c *BardChatbot) PrepareForTelegramMarkdown(msg string) string {
	result := strings.ReplaceAll(msg, "\n* ", "\n*--* ")
	result = strings.ReplaceAll(result, "**", "*")

	if strings.Count(result, "```")%2 == 1 {
		result += "\n```"
	}
	//result = prettyFormatTables(result)
	return result
}

func prettyFormatTables(input string) string {
	// Regular expression to match tables (adapted from https://regex101.com/library/4T4tL6)
	tableRegex := regexp.MustCompile(`\|(.+)\|\n\|[-:]+\|\n((\|.*\|\n)+)`)

	// Find all tables in the input string and replace them with their pretty formatted version
	result := tableRegex.ReplaceAllStringFunc(input, func(match string) string {
		// Split the table into rows and columns
		rows := strings.Split(strings.Trim(match, "\n"), "\n")
		columns := make([][]string, len(rows))
		for i, row := range rows {
			columns[i] = strings.Split(strings.Trim(row, "| "), "|")
		}

		// Create a buffer to hold the pretty formatted table
		buf := bytes.NewBuffer(nil)

		// Use tablewriter to pretty format the table
		table := tablewriter.NewWriter(buf)
		table.SetHeader(columns[0])
		table.SetBorders(tablewriter.Border{Left: true, Top: false, Right: true, Bottom: false})
		table.SetCenterSeparator("|")
		table.SetAutoWrapText(false)
		for _, row := range columns[2:] {
			table.Append(row)
		}
		table.Render()

		// Return the pretty formatted table
		return "```\n" + buf.String() + "```\n"
	})

	return result
}
